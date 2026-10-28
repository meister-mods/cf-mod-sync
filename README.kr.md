# cfmodsync — Forge 버전 기반 모드 캐시 서버와 Windows 클라이언트

[English README](README.md)

Go 서버가 CurseForge API 키와 검증된 JAR 캐시를 관리합니다. 영문 WinForms·WebView2 클라이언트에서 모드 목록·웹 링크를 담고, Forge 버전을 지정한 뒤 호환성 보고서를 확인해 다운로드할 수 있습니다.

## 폴더 구조

```text
server/                         Go 서버·CLI·호환성 엔진·테스트·go.mod
  .env.example                  서버 설정 예제
client/
  .mods.example                 프로젝트 목록 예제
  CfModSync.Client/             Windows 클라이언트
  CfModSync.Client.Checks/      클라이언트·프로토콜 검사
scripts/                        빌드·실행 스크립트
artifacts/                      생성된 실행 파일·배포 ZIP
```

개인 설정은 `server/.env`, 개인 모드 목록은 `client/.mods`에 둡니다. 키·개인 목록·캐시·다운로드는 Git에서 제외됩니다. 배포 ZIP은 새 임시 배포 폴더에서 만들므로 기존 실행 폴더의 개인 데이터가 포함되지 않습니다.

## 빠른 시작

빌드에는 `server/go.mod`에 명시된 Go 버전과 Windows용 .NET 10 SDK가 필요합니다. 클라이언트 실행에는 [.NET 10 Desktop Runtime](https://dotnet.microsoft.com/ko-kr/download/dotnet/10.0)과 [WebView2 Runtime](https://developer.microsoft.com/microsoft-edge/webview2/)이 필요합니다.

저장소 루트에서 서버를 설정하고 실행합니다.

```powershell
if (!(Test-Path server/.env)) { Copy-Item server/.env.example server/.env }
notepad server/.env
# 승인된 CURSEFORGE_API_KEY 설정
go -C server run . serve
```

다른 터미널에서 빌드하고 클라이언트를 실행합니다.

```powershell
.\scripts\build.ps1
.\artifacts\client\CfModSync.Client.exe
```

배포 파일은 `artifacts/cfmodsync-windows.zip`입니다. 배포 환경에서는 서버 실행 파일 옆에 `.env`를 설정하고 `Start-Server.ps1`을 실행합니다. Windows 사용자에게는 `client/` 폴더 전체를 전달합니다.

## 클라이언트 사용

1. **Server**, 필요한 경우 **Access token**을 입력하고 **Connect**를 누릅니다.
2. **Minecraft**, **Forge version**을 선택합니다. 표준 Minecraft 설치 경로에서 감지한 Forge 버전이 표시되며, 직접 입력해도 됩니다. 실제 설치 버전(예: `47.4.28`)을 지정하세요. 클라이언트는 다운로드 전에 Forge 버전 지정을 요구합니다. API·CLI에서 생략하면 로더 미검사 경고를 표시합니다.
3. **Release policy**를 정합니다. 기본값은 `release`입니다. **Import list**로 `.mods`를 불러오거나 URL을 붙여 넣고 웹뷰에서 링크를 담습니다. **Add mod links when clicked**를 끄면 프로젝트 페이지를 탐색할 수 있습니다.
4. **Installed mods**에는 기존 JAR이 실제로 들어 있는 폴더를 선택합니다. 클라이언트는 파일의 메타데이터·SHA-256만 전달합니다. JAR 자체나 클라이언트 파일 경로는 서버로 전송하지 않습니다. 디렉터리 목록 텍스트만으로 내부 의존성을 검사할 수는 없습니다.
5. **Save to**에 새 출력 폴더를 지정하고 **Download selected mods**를 누릅니다. 서버는 전체 조합을 검증한 뒤 다운로드 경로를 제공합니다.
6. **Validation report**에서 누락·버전 충돌·자동 추가 사유·프리릴리스·다운그레이드·계열 교체를 확인합니다. **Allow reviewed replacements**는 보고서를 확인한 변경을 한 번 승인하는 옵션입니다. Forge 버전·필수 의존성 오류는 우회하지 않습니다.

선택 목록은 프로젝트 이름으로 정렬됩니다. 진행 상태·로그·검증 표는 분리되어 있으며, 화면 배율에 따른 크기 조정과 브라우저·목록 패널 너비 조절을 지원합니다.

로컬 파일은 SHA-256 검증 후 재사용합니다. 출력 폴더에 선택되지 않은 JAR이 남아 있으면 이전 버전 중복을 방지하기 위해 차단합니다. 실패 시 `failed-job-<id>.json`을 기록하고 이전 성공 `manifest.json`은 보존합니다. 설정은 `%LOCALAPPDATA%\cfmodsync\settings.json`에 저장하며, 접속 토큰은 저장하지 않습니다.

## 버전 선택과 검증 기준

- CurseForge 프로젝트를 정확한 URL로 식별하고 Minecraft 버전·Forge 로더를 필터링합니다. 실패한 항목을 포함해 요청한 모든 프로젝트를 결과에 남깁니다.
- 후보 JAR을 캐시에 받아 HTTPS·파일명·크기·API SHA-1/MD5·ZIP/CRC를 검증합니다.
- `META-INF/mods.toml`, 매니페스트 주 영역, 선언된 JarJar 라이브러리를 읽습니다. Minecraft·지정 Forge·필수 모드·설치된 선택 의존성·공유 라이브러리의 버전 범위를 검사합니다. 매니페스트 버전 치환, 와일드카드와 Maven 권장 버전 표기도 처리합니다. 기본 FML 로더는 Forge 주 버전으로 검사하며, 별도 언어 로더는 미검사 경고를 표시합니다.
- 동일한 모드 ID를 가진 후보는 JAR에 선언된 버전을 Maven 방식으로 비교합니다. 버전이 같으면 등록 시각·파일 ID를 사용합니다. 요구 조건이 맞지 않으면 다른 후보를 시도하며, 요청 목록 순서대로 우선순위를 둡니다.
- 프로젝트의 모드 ID·콘텐츠 계열이 바뀌거나 버전을 식별할 수 없으면 경고와 함께 등록 시각을 사용합니다. 이런 팩은 파일 고정을 사용하세요.
- `release`는 API beta/alpha와 명확한 선언상 프리릴리스를 제외합니다. `beta`는 API release/beta를 허용하되 선언상 alpha를 제외합니다. `all`은 API alpha도 허용합니다. 파일을 직접 고정하면 배포 채널을 명시적으로 재정의하지만 무결성·Forge·의존성 검사는 계속 적용합니다.
- 기존 폴더와 비교해 누락된 모드 ID, 다운그레이드, 파일명 계열 변경, 같은 버전의 다른 바이트, 식별되지 않는 콘텐츠 팩 누락을 보고합니다. 검토가 필요한 변경은 승인 전 다운로드를 차단합니다.
- 프로젝트당 후보는 기본 200개입니다. 초과 시 임의로 잘라 선택하지 않고 실패합니다. `-candidate-limit`를 최대 1000까지 높이거나 파일을 고정할 수 있습니다. 전체 프로젝트는 500개, 조합 탐색은 10000단계로 제한합니다.

최초 검사는 여러 후보 버전을 다운로드할 수 있습니다. 이후에는 영구 캐시를 재사용합니다. 캐시 용량 정리는 수동입니다. 이 검사는 선언된 메타데이터를 대상으로 하며, Java·런타임 기능, 믹스인, 콘텐츠 팩 내부 항목 삭제, 실제 월드·게임 로딩까지 보증하지는 않습니다.

Forge 참고 자료: [모드 메타데이터·의존성](https://docs.minecraftforge.net/en/1.20.x/gettingstarted/modfiles/), [버전 규칙](https://docs.minecraftforge.net/en/1.20.1/gettingstarted/versioning/), [JarJar 버전 협상](https://docs.minecraftforge.net/en/fg-6.x/dependencies/jarinjar/).

## 모드 목록과 파일 고정

```text
# 호환되는 최고 모드 버전 선택
https://www.curseforge.com/minecraft/mc-mods/jei

# 특정 파일 고정 예시: 기존에 사용한 Kaminari 파일
https://www.curseforge.com/minecraft/mc-mods/kaminari-motor-works/files/6549423
```

빈 줄과 `#` 주석은 무시합니다. 같은 프로젝트에 서로 다른 파일을 고정하면 오류를 표시합니다. CurseForge 필수 관계의 의존성은 기본으로 추가하고, `required_by`에 추가 사유를 기록합니다. 이 관계에 JAR 필수 의존성이 누락된 경우 모드 ID에서 만든 정확한 프로젝트 슬러그를 조회하고, 다운로드한 공급자의 실제 모드 ID까지 검증해 추가합니다. 찾지 못한 의존성은 검증 실패로 남으므로 프로젝트 URL을 목록에 직접 추가해야 합니다. 다른 관계는 자동 추가하지 않습니다. 자동 추가를 끄면 완전한 목록을 직접 제공해야 하며, JAR 내부 의존성 검사는 유지됩니다.

## 단독 CLI·오프라인 검사

다음은 저장소 루트에서 실행하는 명령입니다. `go -C server`로 실행하므로 상대 런타임 경로는 `server/` 기준입니다.

```powershell
if (!(Test-Path client/.mods)) { Copy-Item client/.mods.example client/.mods }
go -C server run . -forge-version 47.4.28 -channel release
go -C server run . -forge-version 47.4.28 -baseline "C:\mods-before" -output downloads/new-profile
go -C server run . -dry-run
go -C server run . -verify-only
go -C server run . audit -mods "C:\mods-after" -baseline "C:\mods-before" -forge-version 47.4.28 -report reports/audit.json
```

오프라인 검사는 API 없이 실제 JAR을 읽어 로컬 SHA-256과 ZIP/CRC를 확인합니다. API 체크섬이 없으면 배포 출처의 진위까지 확인하지는 않습니다. 파일 목록만 있으면 이름·크기 비교만 가능하며 내부 의존성 검사는 불가능합니다.

| CLI 옵션 | 설명 |
| --- | --- |
| `-version` | Minecraft 버전, 기본 `1.20.1` |
| `-forge-version` | 설치 Forge 버전, 환경변수 `FORGE_VERSION` 지원 |
| `-channel` | `release`·`beta`·`all`, `RELEASE_CHANNEL` 설정이 기본값보다 우선 |
| `-input` | 목록 파일, 기본 `../client/.mods` |
| `-output` | 선택 JAR 출력, 기본 `downloads/<MC>-forge` |
| `-baseline` | 기존 JAR 폴더 |
| `-allow-reviewed-changes` | 보고서를 확인한 기존 모드 변경 승인 |
| `-candidate-limit` | 프로젝트당 후보 검사 상한 |
| `-deps=false` | CurseForge 의존성 자동 추가 해제 |
| `-workers` | 동시 후보 다운로드, 1~16 |
| `-max-file-mb` | JAR당 최대 MiB, 기본 1024 |
| `-force` | 같은 이름의 선택 파일을 안전하게 다시 받아 교체 |
| `-prune` | 성공 후 이전 매니페스트에서 관리하던 불필요한 JAR만 검증·삭제 |
| `-dry-run` | API 계획만 확인, 실제 JAR·Forge 호환성은 `not_checked` |
| `-verify-only` | 오프라인 무결성·선언 의존성 재검사 |

후보는 `<output>/.candidate-cache/`에 보관하고 선택 파일만 출력 루트에 설치합니다. CLI 실패 시 `failed-run-*.json`에 기록하고 기존 성공 매니페스트는 보존합니다. Prune은 관리되지 않거나 변경된 파일을 삭제하지 않습니다.

## 서버 설정·API

| 서버 옵션 / 환경변수 | 기본값 |
| --- | --- |
| `-listen` / `SERVER_LISTEN` | `127.0.0.1:8080` |
| `-cache` / `SERVER_CACHE_DIR` | `cache` |
| `-token` / `SERVER_TOKEN` | 루프백에서 빈 값 |
| `-workers` | `4` |
| `-candidate-limit` | `200` |
| `-metadata-ttl` | `15m` |
| `-job-ttl` | `1h` |
| `-max-file-mb` | `1024` |
| `-tls-cert` / `-tls-key` | HTTPS 인증서·개인 키 PEM |

루프백 외부에서는 32자 이상의 토큰이 필요합니다. 원격 클라이언트는 직접 TLS 또는 HTTPS 역방향 프록시를 사용해야 합니다. 클라이언트는 루프백에만 일반 HTTP를 허용합니다. CurseForge API 키와 원본 다운로드 URL은 서버에 둡니다. 캐시 폴더당 서버 프로세스 하나를 사용하세요.

캐시는 `cache/files/<mod-id>/<file-id>/`와 `cache/selections/`에 저장합니다. 같은 파일 요청은 다운로드를 공유하며, 한 작업을 취소해도 다른 요청에 필요한 캐시 채우기는 유지됩니다. 동시 작업은 8개, 보관 기록은 최대 128개이고 완료 기록은 `-job-ttl` 후 정리합니다. 파일·선택 결과는 재시작 후 유지되며 작업 ID는 유지되지 않습니다.

| 메서드 | 경로 |
| --- | --- |
| `GET` | `/healthz` — 공개 상태 확인 |
| `GET` | `/api/v1/status` — 인증 연결 확인 |
| `POST` | `/api/v1/jobs` — 비동기 작업 생성 |
| `GET` | `/api/v1/jobs/{id}` — 진행·검증 결과 |
| `DELETE` | `/api/v1/jobs/{id}` — 취소 |
| `GET` | `/api/v1/files/{modId}/{fileId}` — 검증 JAR, HTTP Range 지원 |

```json
{
  "project_urls": ["https://www.curseforge.com/minecraft/mc-mods/jei"],
  "minecraft_version": "1.20.1",
  "forge_version": "47.4.28",
  "release_channel": "release",
  "include_dependencies": true,
  "refresh": false,
  "allow_reviewed_changes": false
}
```

선택 사항인 `installed_files`에는 `filename`, `sha256`, `mods_toml`, 주 영역 `manifest` 문자열을 전달합니다. 요청은 8 MiB와 프로젝트·기존 파일 각각 500개로 제한됩니다. 상태는 `queued`, `resolving`, `validating`, `completed`, `failed`, `cancelled`이며 후보 검사 상세 진행을 제공합니다. 실패하거나 검토가 승인되지 않은 조합에는 다운로드 경로를 제공하지 않습니다.

## API 접근·개발

[승인된 CurseForge API 키](https://support.curseforge.com/support/solutions/articles/9000208346-about-the-curseforge-api-and-how-to-apply-for-a-key)를 직접 준비해야 합니다. [공식 API](https://docs.curseforge.com/rest-api/)와 프로젝트 배포 허용 설정을 따르며, 누락·차단 파일을 보고합니다. 파일 고정도 배포 제한을 우회하지 않습니다.

```powershell
go -C server test ./...
go -C server vet ./...
.\scripts\build.ps1
```

빌드 스크립트는 로컬 모의 CurseForge 서비스를 사용하는 Go 서버에 실제 C# 클라이언트를 연결해 검사하고, 실행 파일과 의존성 라이선스를 함께 압축합니다. `-Race`는 CGO와 지원 C 컴파일러가 필요합니다. 회귀 검사는 Forge 버전별 최고 호환 후보, 의존성 재탐색, 요청 누락, 기존 폴더 검토, 캐시 유지·복구·공유, 인증 정보 비노출, 클라이언트 해시 검증 등을 포함합니다.
