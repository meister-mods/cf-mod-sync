# CurseForge Forge 모드 자동 다운로더 (Go)

`mods.txt`에 CurseForge Minecraft **모드 프로젝트 URL**을 한 줄씩 넣으면, 프로젝트를 검색해 **Minecraft 버전 + Forge 로더**에 맞는 가장 최근 등록된 JAR를 다운로드합니다.

- 기본 환경: **Minecraft 1.20.1 / Forge**. `.env`의 `MC_VERSION`이나 `-version`으로 변경할 수 있습니다.
- 공식 [CurseForge Core API](https://docs.curseforge.com/rest-api/)를 사용합니다. 웹 HTML 스크래핑, `/download/latest` 리다이렉트 추측, URL·파일 ID 하드코딩은 사용하지 않습니다.
- 서버 API에서 `gameVersion`, `modLoaderType=1`을 필터링하고 결과의 버전/로더 표시를 재확인합니다. 정렬 기준은 **파일 등록 시각(FileDate)**이며, 게임 실행 호환성 자체를 보증하지는 않습니다.
- 필수 의존성(CurseForge의 `relationType=3`)은 기본적으로 재귀적으로 검색해 추가합니다. 다른 관계는 자동 추가하지 않습니다.
- 다운로드 검증: HTTPS, 안전한 파일명, 파일 길이, CurseForge가 제공한 **SHA-1/MD5** 체크섬, JAR/ZIP 내부 파일 CRC·압축 해제 검사. 로컬 SHA-256을 매니페스트에 기록합니다.
- 체크섬이 제공되지 않는 파일은 *검증 불가능*으로 보고 기본적으로 거부합니다. 해시와 ZIP 검사 통과가 곧 악성코드 검사나 Minecraft 부팅 성공을 뜻하지는 않습니다.
- 이미 다운받은 같은 파일은 재검증 후 `cached`로 재사용하며, 손상된 기존 파일은 자동 덮어쓰지 않습니다(`-force`로 재다운로드 가능).
- `manifest.json` 기록, `-dry-run` 계획 확인, `-verify-only` 오프라인 무결성 재검증, `-prune` 이전 실행에서 관리하던 오래된 버전 파일 정리.

## 1. API 키 필요

**2026년 7월 16일부터 CurseForge CDN 직접 다운로드에는 API 키가 필요합니다.** CurseForge REST API 검색도 `x-api-key` 인증을 요구합니다. 공식 정책 및 가이드:

- [CurseForge: CDN API key authentication](https://blog.curseforge.com/introducing-api-key-authentication-for-curseforge-file-downloads/)
- [API key 신청 절차](https://support.curseforge.com/support/solutions/articles/9000208346-about-the-curseforge-api-and-how-to-apply-for-a-key)
- [API 문서](https://docs.curseforge.com/rest-api/)

승인된 API 키를 직접 준비해야 합니다. 이 예제는 타인의 키를 제공하지 않으며 비공식 우회 API를 사용하지 않습니다. 프로젝트 소유자가 **third-party distribution**을 차단하면 API에서 파일을 제공하지 않을 수 있으며, 이 프로그램은 해당 설정을 우회하지 않습니다.

## 2. 설치 및 사용 (Windows PowerShell)

프로젝트 파일이 있는 폴더에서 실행합니다. Go 1.23 이상이 설치되어 있으면 소스로 실행할 수 있습니다. Windows `.exe` 빌드 파일도 별도 제공됩니다.

```powershell
Copy-Item .env.example .env
notepad .env      # CURSEFORGE_API_KEY=... 입력
notepad mods.txt  # 다운로드할 프로젝트 URL 목록 편집

go run . -dry-run          # API에서 호환되는 최신 릴리스만 선택하고 plan.json 출력
go run .                   # JAR 자동 다운로드 + 검증
go run . -verify-only      # 저장된 manifest.json의 파일 오프라인 재검증
go run . -force            # 기존 동일 파일을 안전하게 재다운로드
```

소스 대신 바이너리 사용 시 `go run .`을 `./cfmodsync.exe`로 교체합니다.

설정 예시(`.env`):

```dotenv
CURSEFORGE_API_KEY=YOUR_APPROVED_KEY
MC_VERSION=1.20.1
RELEASE_CHANNEL=all
MOD_LIST=mods.txt
AUTO_DEPS=true
```

`OUTPUT_DIR`를 생략하면 `downloads/1.20.1-forge/`처럼 **MC 버전별로 분리**합니다. 1.21.1로 변경하면 기본 폴더도 `downloads/1.21.1-forge/`로 변합니다.

입력(`mods.txt`) 예시:

```text
# 한 줄에 하나. 프로젝트 URL 이외의 download/latest, /files/123 등을 쓰지 않습니다.
https://www.curseforge.com/minecraft/mc-mods/create
https://www.curseforge.com/minecraft/mc-mods/jei
```

이 패키지의 **`mods.txt`에는 이전에 정리한 93개 프로젝트 URL**을 미리 채워 두었습니다. 사용자가 원하는 모드만 남겨도 됩니다. `mods.example.txt`는 아주 짧은 예제입니다.

## 3. 명령줄 옵션

| 옵션 | 설명 |
| --- | --- |
| `-version 1.21.1` | `.env` 대신 특정 Minecraft 버전 선택 |
| `-input my-mods.txt` | 프로젝트 URL 목록 파일 변경 |
| `-output ./my-mods` | JAR 출력 폴더 변경 |
| `-channel release` | 안정 릴리스만 선택 (`beta`: release+beta, `all`: alpha 포함) |
| `-deps=false` | CurseForge 필수 의존성 자동 추가 해제 |
| `-workers 4` | 동시 다운로드 개수 (1~16) |
| `-max-file-mb 1024` | JAR당 최대 다운로드 크기 MiB |
| `-dry-run` | API 조회 후 다운로드 없이 `plan.json` 출력 |
| `-verify-only` | 인터넷/API 키 없이 `manifest.json` 기준 파일 재검증 |
| `-force` | 같은 이름의 기존 파일을 새로 받아 검증 후 교체 |
| `-prune` | 성공한 실행 이후 **이전 매니페스트에서 관리되던** 불필요한 JAR만 검증하고 삭제 |

`-prune`은 완전히 성공한 다운로드의 마지막 단계에서만 실행됩니다. `.minecraft/mods` 폴더에서 직접 실행하는 것을 권장하지 않습니다. 먼저 별도의 출력 폴더에서 다운로드·검증한 뒤 필요한 파일을 복사하십시오.

## 4. 선택·검증 규칙

1. 사용자가 제공한 URL 호스트와 경로를 검사하며 정확한 `slug`만 추출합니다.
2. CurseForge `GET /v1/mods/search?gameId=432&classId=6&slug=...`에서 정확한 프로젝트 ID를 확인합니다.
3. `GET /v1/mods/{modId}/files?gameVersion=<MC_VERSION>&modLoaderType=1`를 **페이지 끝까지** 조회합니다.
4. `isAvailable`, 게임 버전 일치, `.jar`, Forge 여부, 서버팩·소스팩·미공개 파일 제외 등을 검사합니다. 로더 태그가 완전히 없는 파일은 서버의 Forge 필터링 결과를 신뢰합니다.
5. 허용된 릴리스 채널의 후보 중 `FileDate`가 가장 최근인 파일을 선택합니다. 등록 시점이 같으면 File ID로 순서를 결정합니다.
6. `GET /v1/mods/{modId}/files/{fileId}/download-url`의 **실제 API 다운로드 URL**을 사용합니다. 프로젝트 페이지의 `/download/latest`는 사용하지 않습니다.
7. 다운로드는 `.partial` 임시 파일로 수행합니다. 크기·SHA-1/MD5·ZIP/CRC 검증 뒤에만 `.jar`로 이동시킵니다. CDN 인증은 `x-api-key` 헤더를 사용하고 타 도메인으로 리다이렉트될 경우 해당 헤더를 제거합니다.
8. 실패 항목은 `manifest.json`의 `errors`에 기록하고 계속 진행합니다. 실행 종료 코드는 실패가 하나라도 있으면 `1`입니다.

## 5. 한계 및 주의사항

- **공식 API 키 없이는 실제 외부 다운로드 검증이 불가능합니다.** 코드는 로컬 모의 HTTP 서버를 이용해 단위·통합 테스트했습니다. 실제 93개 프로젝트의 API 응답과 바이너리 다운로드는 인증된 키가 없어 실행하지 않았습니다.
- 태그 호환성은 API 게시자가 제공한 메타데이터에 의존하며, `mods.toml` 안의 Forge 최소 버전·다른 모드의 버전 범위·충돌 여부까지 자동으로 해결하지는 않습니다.
- **차량 콘텐츠 팩과 번들 프로젝트**는 같은 CurseForge 프로젝트에서 최신 버전의 팩 계열 자체가 바뀔 수 있습니다. 예: Blue Seoul Vehicles에서 Garam → Narae 계열. 프로젝트 전체의 `1.20.1/Forge 최신`이 설치했던 팩의 후계 버전임을 보장하지 않습니다. 필요한 경우 특정 파일을 수동으로 고정하세요.
- `RELEASE_CHANNEL=all`은 alpha/beta까지 포함하므로 실제 플레이용은 필요에 따라 `beta` 또는 `release`로 제한하는 편이 안전합니다. 다만 snapshot/alpha만 배포하는 프로젝트는 선택되지 않을 수 있습니다.
- `-prune` 없이 업데이트하면 같은 모드의 오래된 JAR가 출력 폴더에 남을 수 있습니다. 이미 Minecraft mods 폴더인 곳에 직접 다운로드하는 것은 권장하지 않습니다.
- Forge와 NeoForge는 같은 것이 아니며, `MC_VERSION` 변경은 **Forge의 Minecraft 대상 버전만** 변경합니다. NeoForge로 자동 전환하지 않습니다.

## 6. 개발 및 테스트

표준 라이브러리만 사용합니다(외부 Go 모듈 없음).

```bash
go test -race ./...
go vet ./...
go build -o cfmodsync .
```

테스트 항목: URL 파싱, 최신 파일 선택, Minecraft 버전/로더 배제, 배포 채널, SHA-1 무결성, ZIP CRC, API 모의 서버 통합 다운로드, 필수 의존성 추가, 재실행 캐시 및 손상 파일 복구, 인증 헤더의 크로스 도메인 리다이렉트 차단.
