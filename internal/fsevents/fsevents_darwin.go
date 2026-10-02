//go:build darwin && cgo

package fsevents

/*
#cgo LDFLAGS: -framework CoreServices -framework CoreFoundation
#include <CoreServices/CoreServices.h>
#include <dispatch/dispatch.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>

typedef struct {
	char *path;
	uint32_t flags;
	uint64_t id;
} lu_event;

// lu_replay collects the events of one history replay. The stream callback
// only copies them (it runs on the stream's dispatch queue: a slow consumer
// makes fseventsd drop events); Go reads them once the replay is over.
typedef struct {
	lu_event *ev;
	size_t n, cap, limit;
	int overflow;
	int finished; // only touched on the stream's serial queue
	dispatch_semaphore_t done;
} lu_replay;

static void lu_noop(void *arg) { (void)arg; }

static void lu_finish(lu_replay *r) {
	r->finished = 1;
	dispatch_semaphore_signal(r->done);
}

static void lu_callback(ConstFSEventStreamRef stream, void *info, size_t n, void *paths,
                        const FSEventStreamEventFlags flags[], const FSEventStreamEventId ids[]) {
	(void)stream;
	lu_replay *r = (lu_replay *)info;
	char **ps = (char **)paths;
	for (size_t i = 0; i < n && !r->finished; i++) {
		if (flags[i] & kFSEventStreamEventFlagHistoryDone) {
			lu_finish(r); // live events are not wanted
			return;
		}
		if (r->n == r->cap) {
			size_t cap = r->cap ? 2 * r->cap : 1024;
			lu_event *ev = cap <= r->limit ? realloc(r->ev, cap * sizeof *ev) : NULL;
			if (ev == NULL) {
				r->overflow = 1;
				lu_finish(r);
				return;
			}
			r->ev = ev;
			r->cap = cap;
		}
		char *p = strdup(ps[i] != NULL ? ps[i] : "");
		if (p == NULL) {
			r->overflow = 1;
			lu_finish(r);
			return;
		}
		r->ev[r->n].path = p;
		r->ev[r->n].flags = (uint32_t)flags[i];
		r->ev[r->n].id = (uint64_t)ids[i];
		r->n++;
	}
}

static lu_replay *lu_replay_new(size_t limit) {
	lu_replay *r = calloc(1, sizeof *r);
	if (r != NULL) {
		r->limit = limit;
		r->done = dispatch_semaphore_create(0);
	}
	return r;
}

static void lu_replay_free(lu_replay *r) {
	for (size_t i = 0; i < r->n; i++) {
		free(r->ev[i].path);
	}
	free(r->ev);
	dispatch_release(r->done);
	free(r);
}

static lu_event *lu_replay_event(lu_replay *r, size_t i) { return &r->ev[i]; }

// lu_replay_reset forgets the collected events (to replay again).
static void lu_replay_reset(lu_replay *r) {
	for (size_t i = 0; i < r->n; i++) {
		free(r->ev[i].path);
	}
	r->n = 0;
	r->overflow = 0;
	r->finished = 0;
	while (dispatch_semaphore_wait(r->done, DISPATCH_TIME_NOW) == 0) {
	}
}

// lu_replay_dropped reports whether events were coalesced or dropped on the
// way (MustScanSubDirs with UserDropped or KernelDropped).
static int lu_replay_dropped(lu_replay *r) {
	for (size_t i = 0; i < r->n; i++) {
		if (r->ev[i].flags & (kFSEventStreamEventFlagUserDropped | kFSEventStreamEventFlagKernelDropped)) {
			return 1;
		}
	}
	return 0;
}

// lu_replay_run replays the history since `since` for roots. Returns 0 once
// the history was collected, -1 when the stream could not be created or
// started, -2 when *cancel became non-zero, -3 on timeout, -4 when there
// were more than `limit` events or memory ran out.
static int lu_replay_run(lu_replay *r, char **roots, int nroots, uint64_t since, int64_t timeout_ns, volatile int *cancel) {
	CFMutableArrayRef paths = CFArrayCreateMutable(NULL, nroots, &kCFTypeArrayCallBacks);
	if (paths == NULL) {
		return -1;
	}
	for (int i = 0; i < nroots; i++) {
		CFStringRef s = CFStringCreateWithFileSystemRepresentation(NULL, roots[i]);
		if (s == NULL) {
			CFRelease(paths);
			return -1;
		}
		CFArrayAppendValue(paths, s);
		CFRelease(s);
	}
	FSEventStreamContext sc;
	memset(&sc, 0, sizeof sc);
	sc.info = r;
	FSEventStreamRef stream = FSEventStreamCreate(NULL, lu_callback, &sc, paths, (FSEventStreamEventId)since, 0.0,
		kFSEventStreamCreateFlagNoDefer | kFSEventStreamCreateFlagFileEvents);
	CFRelease(paths);
	if (stream == NULL) {
		return -1;
	}
	dispatch_queue_t q = dispatch_queue_create("lu-cleaner.fsevents", DISPATCH_QUEUE_SERIAL);
	FSEventStreamSetDispatchQueue(stream, q);
	int rc;
	if (!FSEventStreamStart(stream)) {
		rc = -1;
	} else {
		uint64_t start = clock_gettime_nsec_np(CLOCK_UPTIME_RAW);
		for (;;) {
			if (dispatch_semaphore_wait(r->done, dispatch_time(DISPATCH_TIME_NOW, 20 * NSEC_PER_MSEC)) == 0) {
				rc = 0;
				break;
			}
			if (*cancel) {
				rc = -2;
				break;
			}
			if ((int64_t)(clock_gettime_nsec_np(CLOCK_UPTIME_RAW) - start) >= timeout_ns) {
				rc = -3;
				break;
			}
		}
		FSEventStreamStop(stream);
	}
	FSEventStreamInvalidate(stream);
	// No callback may run once we return: the events are read then.
	dispatch_sync_f(q, NULL, lu_noop);
	FSEventStreamRelease(stream);
	dispatch_release(q);
	if (rc == 0 && r->overflow) {
		rc = -4;
	}
	return rc;
}

static uint64_t lu_current_event_id(void) {
	return (uint64_t)FSEventsGetCurrentEventId();
}

// lu_volume_uuid writes the UUID of the FSEvents database of dev into buf
// (at least 37 bytes); returns 0 on success.
static int lu_volume_uuid(dev_t dev, char *buf, size_t len) {
	CFUUIDRef u = FSEventsCopyUUIDForDevice(dev);
	if (u == NULL) {
		return -1;
	}
	CFStringRef s = CFUUIDCreateString(NULL, u);
	CFRelease(u);
	if (s == NULL) {
		return -1;
	}
	Boolean ok = CFStringGetCString(s, buf, (CFIndex)len, kCFStringEncodingUTF8);
	CFRelease(s);
	return ok ? 0 : -1;
}
*/
import "C"

import (
	"context"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const supported = true

// maxEvents bounds the memory of one replay (about 150 bytes per event).
const maxEvents = 1 << 20

// dropRetries: replays run again when events were dropped on the way.
const dropRetries = 2

func currentEventID() uint64 { return uint64(C.lu_current_event_id()) }

func volumeUUID(path string) string {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return ""
	}
	var buf [64]C.char
	if C.lu_volume_uuid(C.dev_t(st.Dev), &buf[0], C.size_t(len(buf))) != 0 {
		return ""
	}
	return C.GoString(&buf[0])
}

func replay(ctx context.Context, roots []string, since uint64, timeout time.Duration, fn func(Event) bool) bool {
	if ctx.Err() != nil {
		return false
	}
	r := C.lu_replay_new(maxEvents)
	if r == nil {
		return false
	}
	defer C.lu_replay_free(r)

	arr := (**C.char)(C.malloc(C.size_t(len(roots)) * C.size_t(unsafe.Sizeof((*C.char)(nil)))))
	defer C.free(unsafe.Pointer(arr))
	croots := unsafe.Slice(arr, len(roots))
	for i, p := range roots {
		croots[i] = C.CString(p)
	}
	defer func() {
		for _, p := range croots {
			C.free(unsafe.Pointer(p))
		}
	}()
	// C memory: C polls it while Go may set it from another goroutine.
	cancel := (*C.int)(C.malloc(C.size_t(unsafe.Sizeof(C.int(0)))))
	*cancel = 0
	defer C.free(unsafe.Pointer(cancel))
	stop := watchCancellation(ctx, func() { *cancel = 1 })
	// Registered after free: the watcher must finish before its C flag is freed.
	defer stop()
	// Under a heavy load fseventsd coalesces the events it cannot deliver in
	// time into a "must rescan everything" (UserDropped): the history itself
	// is complete, so replay it again a few times before giving up.
	deadline := time.Now().Add(timeout)
	for attempt := 0; ; attempt++ {
		rem := time.Until(deadline)
		if rem <= 0 {
			return false
		}
		rc := C.lu_replay_run(r, arr, C.int(len(roots)), C.uint64_t(since), C.int64_t(rem.Nanoseconds()), cancel)
		if rc != 0 || ctx.Err() != nil {
			return false
		}
		if attempt == dropRetries || C.lu_replay_dropped(r) == 0 {
			break
		}
		C.lu_replay_reset(r)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Duration(50<<(2*attempt)) * time.Millisecond):
		}
	}
	for i := range int(r.n) {
		e := C.lu_replay_event(r, C.size_t(i))
		if !fn(Event{Path: C.GoString(e.path), Flags: uint32(e.flags), ID: uint64(e.id)}) {
			break
		}
	}
	return true
}
