/** A tiny, four-color sprite sheet. Integer coordinates keep every pixel crisp. */
const SCENE_WIDTH = 128;
const SCENE_HEIGHT = 112;
export const PIXEL_SCALE = 3;
export const PIXEL_WIDTH = SCENE_WIDTH * PIXEL_SCALE;
export const PIXEL_HEIGHT = SCENE_HEIGHT * PIXEL_SCALE;
const FRAME_COUNT = 12;
const palette = ['transparent', '#98212c', '#e53135', '#fffaf4', '#ffd8d0'];
export interface PixelRect { x: number; y: number; width: number; height: number; color: string }

export function flagPixels(pose = 0): PixelRect[] {
	const pixels = new Uint8Array(PIXEL_WIDTH * PIXEL_HEIGHT);
	const fill = (x: number, y: number, width: number, height: number, color: number) => {
		for (let py = Math.max(0, y); py < Math.min(PIXEL_HEIGHT, y + height); py++) {
			for (let px = Math.max(0, x); px < Math.min(PIXEL_WIDTH, x + width); px++) {
				pixels[py * PIXEL_WIDTH + px] = color;
			}
		}
	};
	// Keep the same composition on a finer grid, including a one-pixel outline.
	for (let y = 14 * PIXEL_SCALE; y < 96 * PIXEL_SCALE; y++) {
		const x = Math.round((54 - (y / PIXEL_SCALE - 14) / 6) * PIXEL_SCALE);
		fill(x - 1, y, 3, 1, 1);
		fill(x, y, 1, 1, 2);
	}
	fill(53 * PIXEL_SCALE, 13 * PIXEL_SCALE, 3 * PIXEL_SCALE, 3 * PIXEL_SCALE, 1);
	fill(53 * PIXEL_SCALE + 1, 13 * PIXEL_SCALE + 1, 3 * PIXEL_SCALE - 2, 3 * PIXEL_SCALE - 2, 2);
	const cells: { x: number; y: number; color: number }[] = [];
	// Sample continuous curves on the pixel grid, rather than enlarging coarse steps.
	for (let y = 8 * PIXEL_SCALE; y < 82 * PIXEL_SCALE; y++) {
		for (let x = 38 * PIXEL_SCALE; x < 122 * PIXEL_SCALE; x++) {
			const u = x / PIXEL_SCALE - 53 + (y / PIXEL_SCALE - 23) / 6;
			const wave = Math.sin(u * 0.12 - pose * Math.PI * 2 / FRAME_COUNT) * u / 14;
			const v = y / PIXEL_SCALE - 23 + u / 5 - wave;
			if (u < 0 || u >= 64 || v < 0 || v >= 40) continue;
			cells.push({ x, y, color: (Math.floor(u / 4) + Math.floor(v / 4)) % 2 ? 3 : 2 });
		}
	}
	for (const cell of cells) fill(cell.x - 1, cell.y - 1, 3, 3, 1);
	for (const cell of cells) fill(cell.x, cell.y, 1, 1, cell.color);
	// Merge adjacent pixels for a small SVG fallback and fast sprite preparation.
	const rectangles: PixelRect[] = [];
	let previous = new Map<string, PixelRect>();
	for (let y = 0; y < PIXEL_HEIGHT; y++) {
		const current = new Map<string, PixelRect>();
		for (let x = 0; x < PIXEL_WIDTH;) {
			const color = pixels[y * PIXEL_WIDTH + x];
			const start = x++;
			while (x < PIXEL_WIDTH && pixels[y * PIXEL_WIDTH + x] === color) x++;
			if (!color) continue;
			const key = `${start}:${x - start}:${color}`;
			let rect = previous.get(key);
			if (rect) rect.height++;
			else {
				rect = { x: start, y, width: x - start, height: 1, color: palette[color] };
				rectangles.push(rect);
			}
			current.set(key, rect);
		}
		previous = current;
	}
	return rectangles;
}

export function createFlagRenderer(canvas: HTMLCanvasElement) {
	const context = canvas.getContext('2d', { alpha: true });
	if (!context) return null;
	canvas.width = PIXEL_WIDTH;
	canvas.height = PIXEL_HEIGHT;
	context.imageSmoothingEnabled = false;
	context.scale(PIXEL_SCALE, PIXEL_SCALE);
	const sprites = Array.from({ length: FRAME_COUNT }, (_, pose) => {
		const sprite = document.createElement('canvas');
		sprite.width = PIXEL_WIDTH;
		sprite.height = PIXEL_HEIGHT;
		const paint = sprite.getContext('2d')!;
		for (const rect of flagPixels(pose)) {
			paint.fillStyle = rect.color;
			paint.fillRect(rect.x, rect.y, rect.width, rect.height);
		}
		return sprite;
	});
	const rect = (x: number, y: number, width: number, height: number, color: string) => {
		context.fillStyle = color;
		context.fillRect(Math.round(x * PIXEL_SCALE) / PIXEL_SCALE, Math.round(y * PIXEL_SCALE) / PIXEL_SCALE, width, height);
	};
	const ring = (x: number, y: number, width: number) => {
		rect(x + 2, y, width - 4, 2, '#e53135');
		rect(x, y + 2, 2, 7, '#e53135');
		rect(x + width - 2, y + 2, 2, 7, '#e53135');
		rect(x + 2, y + 9, width - 4, 2, '#98212c');
		rect(x + 2, y + 2, 1, 5, '#ffd8d0');
	};
	const sparkle = (x: number, y: number, size: number) => {
		rect(x, y - size, 1, size * 2 + 1, '#e53135');
		rect(x - size, y, size * 2 + 1, 1, '#e53135');
	};
	// A tiny bitmap wordmark avoids loading a font just for the illustration.
	const go = ['011100111001', '110001101101', '110001101101', '110111101101', '110011101100', '110011101101', '011100111001'];
	return {
		draw(seconds: number, windX: number, windY: number) {
			context.clearRect(0, 0, SCENE_WIDTH, SCENE_HEIGHT);
			const beat = seconds % 4.8;
			const boost = Math.max(0, 1 - Math.abs(beat - 1.0) / 0.6);
			const pose = Math.floor(seconds * 18 + windX * 3 + FRAME_COUNT) % sprites.length;
			const entry = Math.round(28 * (1 - Math.min(seconds / 0.7, 1)) ** 3);
			const offset = entry + Math.round(boost * 4);
			const bounce = Math.round(Math.sin(seconds * 5) + windY * 2);
			// Parallax lanes, chunky afterimages and collectible rings.
			for (let lane = 0; lane < 7; lane++) {
				const x = 128 - ((seconds * (38 + lane * 5) + lane * 23) % 156);
				rect(x, 17 + lane * 11, 6 + lane * 2, lane % 3 === 0 ? 2 : 1, lane % 2 ? '#ffd8d0' : '#f3e9e3');
			}
			for (let i = 0; i < 3; i++) {
				const x = 8 + i * 16;
				const y = 48 - i * 9 + Math.round(Math.sin(seconds * 3 + i) * 3);
				const width = [10, 8, 5, 8][Math.floor(seconds * 8 + i) % 4];
				ring(x, y, width);
			}
			context.globalAlpha = 0.07 + boost * 0.08;
			context.drawImage(sprites[pose], offset - 12, bounce + 4, SCENE_WIDTH, SCENE_HEIGHT);
			context.globalAlpha = 1;
			context.drawImage(sprites[pose], offset, bounce, SCENE_WIDTH, SCENE_HEIGHT);
			sparkle(115, 79, 2 + Math.floor(seconds * 3) % 2);
			sparkle(23, 20, 1 + Math.floor(seconds * 4) % 2);
			for (let row = 0; row < go.length; row++) {
				for (let column = 0; column < go[row].length; column++) {
					if (go[row][column] === '1') rect(12 + column, 78 + row, 1, 1, '#e53135');
				}
			}
			// The track uses logical coordinates; sprites retain the finer raster resolution.
			rect(8, 101, 112, 1, '#98212c');
			const scroll = Math.floor(seconds * 22) % 8;
			for (let x = -8; x < 120; x += 8) {
				rect(x + 8 - scroll, 103, 4, 2, '#e53135');
				rect(x + 12 - scroll, 105, 4, 2, '#e53135');
			}
		},
		destroy() { for (const sprite of sprites) { sprite.width = 0; sprite.height = 0; } },
	};
}
