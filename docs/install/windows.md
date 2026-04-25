# Windows 설치 가이드 (Agent)

Windows에서는 Agent만 실행합니다. Hub는 외부에서 접근 가능한 서버에서 실행하세요.

## 사전 준비

- Docker Desktop 설치 및 실행 중
- PowerShell 7+ 권장

## 1. Hub 대시보드에서 Agent 등록

`https://yourdomain.com/admin` → Agents → Add Agent → 토큰 복사

## 2. 바이너리 다운로드 (PowerShell)

```powershell
Invoke-WebRequest -Uri https://github.com/lnyarl/preview/releases/latest/download/agent-windows-amd64.exe `
  -OutFile C:\preview\agent.exe
```

## 3. 실행

```powershell
$IP = (Get-NetIPAddress -AddressFamily IPv4 `
       -InterfaceAlias "Ethernet*","Wi-Fi*" |
       Select-Object -First 1).IPAddress

C:\preview\agent.exe start `
  --hub-url=wss://yourdomain.com/agent/ws `
  --token=agt_여기에_토큰_입력 `
  --repo-url=https://github.com/org/repo.git `
  --advertise-host=$IP `
  --label env=home
```

## 4. 시작 시 자동 실행 (작업 스케줄러)

```powershell
$action = New-ScheduledTaskAction `
  -Execute "C:\preview\agent.exe" `
  -Argument "start --hub-url=wss://yourdomain.com/agent/ws --token=agt_TOKEN --repo-url=https://github.com/org/repo.git --advertise-host=이_머신의_IP --label env=home"

$trigger = New-ScheduledTaskTrigger -AtLogOn

Register-ScheduledTask `
  -TaskName "PreviewAgent" `
  -Action $action `
  -Trigger $trigger `
  -RunLevel Highest `
  -Force
```

또는 NSSM(Non-Sucking Service Manager)을 써서 Windows 서비스로 등록할 수 있습니다:

```powershell
# NSSM 설치: winget install nssm
nssm install PreviewAgent C:\preview\agent.exe
nssm set PreviewAgent AppParameters "start --hub-url=wss://yourdomain.com/agent/ws --token=agt_TOKEN ..."
nssm start PreviewAgent
```
