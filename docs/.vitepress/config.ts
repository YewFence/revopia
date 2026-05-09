import { defineConfig } from 'vitepress'

export default defineConfig({
  base: '/revopia/',
  lang: 'zh-CN',
  title: 'revopia',
  description: '把 Docker volume 稳定暴露给 Kopia 的轻量挂载桥',
  cleanUrls: true,
  lastUpdated: true,

  themeConfig: {
    nav: [
      { text: '快速开始', link: '/guide/getting-started' },
      { text: '恢复', link: '/guide/restore' },
      { text: '命令参考', link: '/guide/commands' },
      { text: 'GitHub', link: 'https://github.com/YewFence/revopia' }
    ],

    sidebar: [
      {
        text: '使用指南',
        items: [
          { text: '快速开始', link: '/guide/getting-started' },
          { text: '恢复数据', link: '/guide/restore' },
          { text: '命令参考', link: '/guide/commands' },
          { text: 'Shell 补全', link: '/guide/completion' }
        ]
      }
    ],

    search: {
      provider: 'local'
    },

    socialLinks: [
      { icon: 'github', link: 'https://github.com/YewFence/revopia' }
    ],

    footer: {
      message: 'Released under the MIT License.',
      copyright: 'Copyright © YewFence'
    },

    docFooter: {
      prev: '上一页',
      next: '下一页'
    },

    outline: {
      label: '本页目录'
    },

    lastUpdated: {
      text: '最后更新'
    }
  }
})
