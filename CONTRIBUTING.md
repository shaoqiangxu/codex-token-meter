# Contributing

感谢你关注 Codex Token Meter。修复缺陷、完善不同平台的日志兼容性、补充测试和改进文档都很欢迎。

## 开始之前

1. 先搜索现有 Issue，避免重复工作。
2. 涉及 Codex 日志解析时，只提交最小化、脱敏后的结构样例；不要提交真实 Prompt、推理正文、工具结果、源码、Cookie、Token 或完整 JSONL。
3. 行为或数据口径变化应同时更新 README 及 `docs/` 中对应文档。

## 本地开发

```bash
git clone https://github.com/shaoqiangxu/codex-token-meter.git
cd codex-token-meter
go test ./...
go vet ./...
node tests/frontend_test.js
go build ./...
```

要求 Go 1.25 或更高版本。前端没有 npm 依赖，测试只需要 Node.js。

## Pull Request 要求

- 一次 PR 解决一个清晰问题。
- 新功能和缺陷修复应包含相应测试。
- 不降低以下安全边界：服务仅监听 loopback、Agent 独立令牌、管理端 CSRF 防护、日志正文不上传。
- 不引入仅为个人规模部署服务的重型基础设施或前端框架。

提交 PR 即表示你同意按本项目的 MIT 许可证贡献代码。
