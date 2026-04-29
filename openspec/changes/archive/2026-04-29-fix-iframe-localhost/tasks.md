## 1. Dashboard JS

- [x] 1.1 In `renderIframeSection` in `internal/gateway/web/app.js`, parse `section.url` with `new URL()`, check if hostname is `localhost` or `127.0.0.1`, and replace with `window.location.hostname` before setting `frame.src`

## 2. Tests

- [x] 2.1 Update `internal/gateway/web_test.go` (or equivalent) if iframe rendering has server-side test coverage; otherwise verify manually by accessing dashboard from a non-localhost browser
