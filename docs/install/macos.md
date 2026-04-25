# macOS 설치 가이드 (Agent)

macOS에서는 Agent만 실행합니다. Hub는 외부에서 접근 가능한 서버에서 실행하세요.

## 1. Hub 대시보드에서 Agent 등록

`https://yourdomain.com/admin` → Agents → Add Agent → 토큰 복사

## 2. 바이너리 다운로드

```bash
# Apple Silicon (M1/M2/M3)
curl -fsSL https://github.com/lnyarl/preview/releases/latest/download/agent-darwin-arm64 -o /usr/local/bin/agent
chmod +x /usr/local/bin/agent

# Intel
curl -fsSL https://github.com/lnyarl/preview/releases/latest/download/agent-darwin-amd64 -o /usr/local/bin/agent
chmod +x /usr/local/bin/agent
```

처음 실행 시 Gatekeeper 경고가 나오면:
```bash
xattr -d com.apple.quarantine /usr/local/bin/agent
```

## 3. 실행

```bash
agent start \
  --hub-url=wss://yourdomain.com/agent/ws \
  --token=agt_여기에_토큰_입력 \
  --repo-url=https://github.com/org/repo.git \
  --advertise-host=$(ipconfig getifaddr en0) \
  --label env=home
```

## 4. 시작 시 자동 실행 (launchd)

```bash
cat > ~/Library/LaunchAgents/com.preview.agent.plist << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.preview.agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/agent</string>
    <string>start</string>
    <string>--hub-url=wss://yourdomain.com/agent/ws</string>
    <string>--token=agt_여기에_토큰_입력</string>
    <string>--repo-url=https://github.com/org/repo.git</string>
    <string>--advertise-host=이_머신의_IP</string>
    <string>--label</string>
    <string>env=home</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/tmp/preview-agent.log</string>
  <key>StandardErrorPath</key>
  <string>/tmp/preview-agent.log</string>
</dict>
</plist>
EOF

launchctl load ~/Library/LaunchAgents/com.preview.agent.plist
```

로그 확인:
```bash
tail -f /tmp/preview-agent.log
```
