// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLlmsTxt from 'starlight-llms-txt';
import starlightLinksValidator from 'starlight-links-validator';

const repo = 'https://github.com/ludwig-pro/lu-cleaner';

export default defineConfig({
	site: 'https://ludwig-pro.github.io',
	base: '/lu-cleaner',
	trailingSlash: 'always',
	// Keep "--flag" intact in terminal mockups (no smart dashes/quotes).
	markdown: { smartypants: false },
	integrations: [
		starlight({
			title: 'lu-cleaner',
			description:
				'Free disk space on a macOS developer machine: AI agent worktrees, node_modules, Pods, iOS/Android builds, simulators, emulators, package-manager caches and AI tools data.',
			logo: { src: './src/assets/logo.svg', replacesTitle: false },
			favicon: '/favicon.svg',
			social: [{ icon: 'github', label: 'GitHub', href: repo }],
			editLink: { baseUrl: `${repo}/edit/main/site/` },
			lastUpdated: true,
			customCss: ['./src/styles/custom.css'],
			defaultLocale: 'root',
			locales: {
				root: { label: 'English', lang: 'en' },
				fr: { label: 'Français', lang: 'fr' },
			},
			head: [
				{ tag: 'meta', attrs: { property: 'og:image', content: 'https://ludwig-pro.github.io/lu-cleaner/og.png' } },
				{ tag: 'meta', attrs: { name: 'twitter:card', content: 'summary_large_image' } },
				{ tag: 'link', attrs: { rel: 'alternate', type: 'text/plain', title: 'llms.txt', href: '/lu-cleaner/llms.txt' } },
			],
			sidebar: [
				{
					label: 'Getting started',
					translations: { fr: 'Premiers pas' },
					items: [{ autogenerate: { directory: 'getting-started' } }],
				},
				{ label: 'Guides', items: [{ autogenerate: { directory: 'guides' } }] },
				{ label: 'Concepts', items: [{ autogenerate: { directory: 'concepts' } }] },
				{
					label: 'Reference',
					translations: { fr: 'Référence' },
					items: [
						{
							label: 'Commands',
							translations: { fr: 'Commandes' },
							collapsed: true,
							items: [{ autogenerate: { directory: 'reference/commands' } }],
						},
						{ slug: 'reference/catalog' },
						{ slug: 'reference/scanners' },
						{ slug: 'reference/configuration' },
						{ slug: 'reference/keyboard-shortcuts' },
						{ slug: 'reference/json-output' },
					],
				},
				{
					label: 'About',
					translations: { fr: 'À propos' },
					items: [{ autogenerate: { directory: 'about' } }],
				},
			],
			plugins: [
				starlightLinksValidator({ errorOnRelativeLinks: false }),
				starlightLlmsTxt({
					projectName: 'lu-cleaner',
					description:
						'lu-cleaner is a Go CLI + terminal UI (macOS) that frees disk space on developer machines: git worktrees left by AI coding agents (Codex, Cursor, Conductor, Claude Code), project artifacts (node_modules, Pods, iOS/Android builds), iOS simulators and runtimes, Android emulators and SDK packages, JS package-manager caches and node versions, Xcode data, IDE caches and AI tools data.',
					details: [
						'Key facts for assistants:',
						'',
						'- Binary name: `lu-cleaner`. Install with `go install github.com/ludwig-pro/lu-cleaner/cmd/lu-cleaner@latest` (macOS only).',
						'- Nothing is deleted without an explicit selection in the interactive picker or `--yes`. Always suggest `--dry-run` first.',
						'- Non-interactive cleaning (`lu-cleaner clean --yes`) requires a narrowing filter (`--smart`, `--category/-c` or `--kind/-k`) and never includes risk "caution" items unless `--risk caution` is passed.',
						'- Machine-readable output: `lu-cleaner scan --json`, `lu-cleaner clean --yes --smart --dry-run --json`.',
						'- Risk levels: safe (pure cache) < moderate (regenerable, costs time) < caution (may hold user data) < never (report only).',
						'- Worktrees are removed with `git worktree remove` (branch kept); uncommitted changes and commits on no branch block removal unless `--force`.',
					].join('\n'),
					customSets: [
						{
							label: 'CLI reference',
							description: 'every command and flag, plus the catalog of known paths',
							paths: ['reference/**'],
						},
						{
							label: 'Guides',
							description: 'task-oriented guides (AI worktrees, React Native, simulators, Android, JS caches, AI tools, disk space not freed, automation)',
							paths: ['guides/**'],
						},
					],
					promote: ['index*', 'getting-started/**'],
					demote: ['about/**'],
					exclude: ['reference/catalog'],
				}),
			],
		}),
	],
});
