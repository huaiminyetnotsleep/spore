import { defineConfig } from 'vitepress'
import { PROJECT_IDENTITY } from './projectIdentity.generated'

// GitHub Pages 项目站点部署在配置的项目子路径下
export default defineConfig({
  lang: 'zh-CN',
  title: PROJECT_IDENTITY.displayName,
  description: PROJECT_IDENTITY.description,
  base: PROJECT_IDENTITY.pagesBase,
  lastUpdated: true,
  vite: {
    publicDir: '../public'
  },
  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: `${PROJECT_IDENTITY.pagesBase}favicon.svg` }],
    ['link', { rel: 'apple-touch-icon', href: `${PROJECT_IDENTITY.pagesBase}icon.svg` }]
  ],
  themeConfig: {
    logo: '/icon.svg',
    nav: [
      { text: '首页', link: '/', activeMatch: '^/$' },
      { text: '项目介绍', link: '/guide/introduction', activeMatch: '/guide/introduction' },
      { text: '快速部署', link: '/guide/deployment', activeMatch: '/guide/(deployment|telegram-api-credentials|github-oauth)' },
      { text: '使用指南', link: '/guide/usage', activeMatch: '/guide/(usage|download)' },
      { text: '配置参考', link: '/reference/configuration', activeMatch: '/reference/' },
      { text: '运维手册', link: '/ops/operations', activeMatch: '/ops/' },
      { text: '开发', link: '/guide/development', activeMatch: '/guide/development' },
      { text: '更新日志', link: 'https://github.com/huaiminyetnotsleep/spore/blob/main/CHANGELOG.md' }
    ],
    sidebar: {
      '/guide/': [
        { text: '项目介绍', link: '/guide/introduction' },
        {
          text: '快速开始',
          items: [
            { text: '快速部署', link: '/guide/deployment' },
            { text: '申请 TG API 凭据', link: '/guide/telegram-api-credentials' },
            { text: 'GitHub OAuth 登录配置', link: '/guide/github-oauth' }
          ]
        },
        {
          text: '使用指南',
          items: [
            { text: 'Bot 使用指南', link: '/guide/usage' },
            { text: '云盘下载', link: '/guide/download' }
          ]
        },
        {
          text: '开发',
          items: [{ text: '本地开发与调试', link: '/guide/development' }]
        }
      ],
      '/reference/': [
        {
          text: '配置与接口',
          items: [
            { text: '配置参考', link: '/reference/configuration' },
            { text: '管理端 API', link: '/reference/api' },
            { text: '数据库设计', link: '/reference/database-schema' }
          ]
        },
        {
          text: '架构与设计',
          items: [
            { text: '架构文档', link: '/reference/architecture' },
            { text: 'SPA 管理端验收契约', link: '/reference/admin-acceptance' }
          ]
        }
      ],
      '/ops/': [
        {
          text: '运维',
          items: [
            { text: '运维手册', link: '/ops/operations' },
            { text: '封禁应急手册', link: '/ops/incidents' },
            { text: '问题与解决记录', link: '/ops/troubleshooting' },
            { text: '发版流程', link: '/ops/release' }
          ]
        }
      ]
    },
    socialLinks: [{ icon: 'github', link: PROJECT_IDENTITY.repositoryUrl }],
    editLink: {
      pattern: `${PROJECT_IDENTITY.repositoryUrl}/edit/main/docs/:path`,
      text: '在 GitHub 上编辑此页'
    },
    search: {
      provider: 'local',
      options: {
        translations: {
          button: { buttonText: '搜索文档', buttonAriaLabel: '搜索文档' },
          modal: {
            noResultsText: '没有找到结果',
            resetButtonTitle: '清除查询',
            footer: { selectText: '选择', navigateText: '切换', closeText: '关闭' }
          }
        },
        // minisearch 默认按空白/标点切词，中文整段会成为单个 token；
        // 这里在 CJK 字符边界处额外切开，保证中文搜索可用。
        miniSearch: {
          options: {
            tokenize(text: string) {
              return text
                .split(/[\s　\p{P}]+|(?=[一-鿿])|(?<=[一-鿿])/u)
                .filter(Boolean)
            }
          }
        }
      }
    },
    outline: { level: [2, 3], label: '本页目录' },
    docFooter: { prev: '上一篇', next: '下一篇' },
    lastUpdated: { text: '最后更新于' },
    returnToTopLabel: '回到顶部',
    sidebarMenuLabel: '菜单',
    darkModeSwitchLabel: '外观',
    lightModeSwitchTitle: '切换到浅色模式',
    darkModeSwitchTitle: '切换到深色模式'
  }
})
