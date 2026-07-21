import { defineConfig } from 'astro/config';
import { unified } from '@astrojs/markdown-remark';
import astroD2 from 'astro-d2';
import starlight from '@astrojs/starlight';
import starlightAnnouncement from 'starlight-announcement';
import prefixBaseLinks from './src/plugins/prefix-base-links.mjs';

const productionBase = process.env.GITHUB_ACTIONS ? '/praxis' : '/';
const docsPrefix = productionBase === '/' ? '' : productionBase;

export default defineConfig({
  site: 'https://shirvan.github.io',
  base: productionBase,
  markdown: {
    processor: unified({ rehypePlugins: [[prefixBaseLinks, { base: productionBase }]] }),
  },
  integrations: [
    astroD2({
      inline: true,
      pad: 48,
      theme: {
        default: '0',
        dark: false,
      },
      experimental: { useD2js: true },
    }),
    starlight({
      title: 'Praxis',
      description: 'A durable AWS infrastructure control plane powered by CUE and Restate.',
      customCss: ['./src/styles/starlight.css'],
      social: [
        { icon: 'github', label: 'GitHub', href: 'https://github.com/shirvan/praxis' },
      ],
      plugins: [
        starlightAnnouncement({
          displayMode: 'first',
          announcements: [
            {
              id: 'praxis-alpha-contract',
              content: 'Praxis is in alpha. The supported API is praxis.io/alpha, and the contract can change while Praxis is in alpha.',
              link: {
                text: 'About the alpha contract',
                href: `${docsPrefix}/docs/start/what-is-praxis/`,
              },
              variant: 'caution',
              dismissible: true,
              showOn: ['/**'],
            },
          ],
        }),
      ],
      components: {
        Footer: './src/components/DocsFooter.astro',
      },
      sidebar: [
        { label: 'Documentation', link: '/docs/' },
        { label: 'Start', items: [{ autogenerate: { directory: 'docs/start' } }] },
        { label: 'Build', items: [{ autogenerate: { directory: 'docs/build' } }] },
        { label: 'Operate', items: [{ autogenerate: { directory: 'docs/operate' } }] },
        { label: 'Reference', items: [
          { label: 'AWS resource catalog', link: '/resources/' },
          { autogenerate: { directory: 'docs/reference' } },
        ] },
        { label: 'Understand', items: [{ autogenerate: { directory: 'docs/understand' } }] },
      ],
      editLink: {
        baseUrl: 'https://github.com/shirvan/praxis/edit/main/site/',
      },
      lastUpdated: true,
      pagefind: true,
    }),
  ],
});
