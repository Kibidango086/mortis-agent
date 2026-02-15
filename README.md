# MortisAgent

> **⚠️ 自用项目 | Personal Use Only**
> 
> 本项目为个人学习使用，基于 [picoclaw](https://github.com/sipeed/picoclaw) 进行二次开发。
> 
> **项目源头**: [sipeed/picoclaw](https://github.com/sipeed/picoclaw) (MIT License)

个人 AI 助手，支持多 Agent、流式对话和丰富的工具系统。

## 声明

本项目代码主要来源于 [picoclaw](https://github.com/sipeed/picoclaw)，遵循原项目的 MIT 开源协议：

- 原始项目：[https://github.com/sipeed/picoclaw](https://github.com/sipeed/picoclaw)
- 原协议：MIT License
- 修改内容：增加流式对话、多 Agent 系统、增强工具等

本项目仅供个人学习和自用，不进行商业分发。

## 特性

- 流式对话: 显示思考过程
- AI 向用户提问功能


## 快速开始

```bash
# 编译
make build

# 初始化
./mortis-agent onboard

# 配置 API Key
vim ~/.mortis-agent/config.json

# 运行
./mortis-agent agent -m "Hello"

# 或启动网关（Telegram）
./mortis-agent gateway
```

## 配置

```json
{
  "agents": {
    "defaults": {
      "model": "gpt-4",
      "provider": "openai"
    }
  },
  "channels": {
    "telegram": {
      "enabled": true,
      "token": "YOUR_BOT_TOKEN",
      "stream_mode": true
    }
  }
}
```

## 工具

- `read/write/edit/append_file` - 文件操作
- `list_dir/glob/grep` - 文件搜索
- `web_search/web_fetch` - 网络搜索
- `todo` - 待办管理
- `question` - 交互式提问
- `exec` - 命令执行

## 命令

```bash
./mortis-agent agent          # 交互式对话
./mortis-agent agent -m "msg" # 单次对话
./mortis-agent gateway        # 启动网关
./mortis-agent status         # 查看状态
./mortis-agent onboard        # 初始化配置
```

## 目录

```
~/.mortis-agent/
├── config.json      # 配置
└── workspace/
    ├── agents/      # Agent 配置
    ├── skills/      # 技能
    └── memory/      # 记忆
```

## 致谢

- 原始项目：[picoclaw](https://github.com/sipeed/picoclaw) by [Sipeed](https://github.com/sipeed)

## License

MIT License (与原始项目 picoclaw 相同)

Copyright (c) 2026 Kibidango086
Copyright (c) 2026 Sipeed (original picoclaw project)
