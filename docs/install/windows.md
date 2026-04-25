# Windows 설치 가이드

## 사전 준비

- Docker Desktop 설치 및 실행 중
- PowerShell 7+ 권장

---

## Hub 설치

### 1. 바이너리 다운로드 (PowerShell)

```powershell
New-Item -ItemType Directory -Force C:\preview
Invoke-WebRequest -Uri https://github.com/lnyarl/preview/releases/latest/download/hub-windows-amd64.exe `
  -OutFile C:\preview\hub.exe
```

### 2. 설정 파일 작성

```powershell
@"
GITHUB_WEBHOOK_SECRET=여기에_시크릿_입력
ADMIN_PASSWORD=여기에_비밀번호_입력
PREVIEW_BASE_DOMAIN=localhost
AGENT_DOWNLOAD_URL=https://github.com/lnyarl/preview/releases/latest/download
DATABASE_URL=sqlite:///C:/preview/hub.db
"@ | Set-Content C:\preview\.env

# DB 초기화
cd C:\preview
.\hub.exe migrate up
```

### 3. 실행 (PowerShell)

```powershell
Get-Content C:\preview\.env | ForEach-Object {
  if ($_ -match '^([^#][^=]*)=(.*)$') {
    [System.Environment]::SetEnvironmentVariable($matches[1].Trim(), $matches[2].Trim(), 'Process')
  }
}
C:\preview\hub.exe
```

### 4. 시작 시 자동 실행 (NSSM 사용)

```powershell
# NSSM 설치
winget install nssm

# Hub를 Windows 서비스로 등록
nssm install PreviewHub C:\preview\hub.exe
nssm set PreviewHub AppEnvironmentExtra `
  GITHUB_WEBHOOK_SECRET=여기에_시크릿_입력 `
  ADMIN_PASSWORD=여기에_비밀번호_입력 `
  DATABASE_URL=sqlite:///C:/preview/hub.db `
  AGENT_DOWNLOAD_URL=https://github.com/lnyarl/preview/releases/latest/download
nssm set PreviewHub AppDirectory C:\preview
nssm set PreviewHub Start SERVICE_AUTO_START
nssm start PreviewHub
```

또는 작업 스케줄러:

```powershell
$action = New-ScheduledTaskAction -Execute "C:\preview\hub.exe"
$trigger = New-ScheduledTaskTrigger -AtStartup
Register-ScheduledTask -TaskName "PreviewHub" -Action $action -Trigger $trigger -RunLevel Highest -Force
```

---

## Agent 설치

### 1. Hub 대시보드에서 Agent 등록

`http://localhost:3000/admin` → Agents → Add Agent → 토큰 복사

### 2. 바이너리 다운로드 (PowerShell)

```powershell
Invoke-WebRequest -Uri https://github.com/lnyarl/preview/releases/latest/download/agent-windows-amd64.exe `
  -OutFile C:\preview\agent.exe
```

### 3. 실행

```powershell
$IP = (Get-NetIPAddress -AddressFamily IPv4 `
       -InterfaceAlias "Ethernet*","Wi-Fi*" |
       Select-Object -First 1).IPAddress

C:\preview\agent.exe start `
  --hub-url=ws://localhost:3000/agent/ws `
  --token=agt_여기에_토큰_입력 `
  --repo-url=https://github.com/org/repo.git `
  --advertise-host=$IP
```

### 4. 시작 시 자동 실행 (NSSM)

```powershell
nssm install PreviewAgent C:\preview\agent.exe
nssm set PreviewAgent AppParameters "start --hub-url=ws://localhost:3000/agent/ws --token=agt_TOKEN --repo-url=https://github.com/org/repo.git --advertise-host=이_머신의_IP"
nssm set PreviewAgent Start SERVICE_AUTO_START
nssm start PreviewAgent
```

로그 확인:
```powershell
nssm status PreviewHub
nssm status PreviewAgent
Get-EventLog -LogName Application -Source nssm -Newest 20
```
