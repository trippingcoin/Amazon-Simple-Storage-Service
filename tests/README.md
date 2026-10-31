# Тесты Triple-S

Все написанные для проекта тесты, локальные mocks, simulation и тестовые helpers находятся здесь. Используется один существующий Go-модуль `A3S`; дополнительных модулей или зависимостей для тестового запуска нет.

## Почему нужен overlay

Storage/recovery-тесты обращаются к закрытым fault-injection hooks и metadata helpers; HTTP-тесты — к закрытым XML/error helpers; CLI и серверные subprocess tests — к функциям пакетов `main`. При обычном переносе в другой Go-пакет эти проверки не скомпилируются.

`go run ./tests` создаёт временный JSON для стандартного `go test -overlay`, сопоставляющий файлы из `_suites/` с виртуальными `_test.go` прежних пакетов. Исходники приложения не копируются, видимость методов не меняется, файлы рядом с production-кодом не создаются. Сопоставление групп задано в `layout.json`; build tags остаются в самих тестах. Путь checkout нормализуется с учётом symlink, что важно для `/var` и `/private/var` на macOS.

Префикс `_` у `_suites` исключает эти каталоги из обычного Go package discovery. **Обычный `go test ./...` без overlay не запускает перенесённые тесты.** CI, локальные проверки и команды ниже используют runner. При прямом запуске из IDE нужен тот же overlay; проще настроить External Tool на приведённые команды. Стек ошибок может показывать виртуальный путь прежнего пакета — реальный файл ищите по группе в `layout.json`.

## Структура и покрываемые компоненты

```text
tests/
├── run.go, layout.json
├── _suites/
│   ├── api/                   # buckets, objects, download, listing/quotas,
│   │                          # authentication, grants, XML, Observatory
│   ├── unit/
│   │   ├── access/            # registry, tokens, grants
│   │   ├── backup/            # archives, SHA-256, restore, unsafe input
│   │   ├── backup_cli/        # CLI create/verify/restore
│   │   ├── cluster/           # cluster config validation
│   │   ├── config/            # server flags, paths, security defaults
│   │   ├── httpapi/           # DI, streaming errors, telemetry, XML
│   │   ├── observatory/       # snapshots, concurrency, dependency failure
│   │   ├── runner/            # overlay discovery and missing-suite guards
│   │   ├── service/           # validation, context and error propagation
│   │   └── storage/           # buckets/objects, metadata, journal, recovery,
│   │                          # quotas, namespaces, filesystem security
│   ├── integration/           # integration tag: HTTP/TLS, process lifecycle,
│   │                          # shutdown, disconnect, users-file restart
│   ├── cluster/               # clusterintegration tag: PostgreSQL + MinIO,
│   │                          # replicas, multipart, ranges, ACL, GC, import
│   ├── stress/                # stress tag: 128 MiB file, concurrent HTTP
│   └── benchmarks/storage/    # PUT, download, listing, metadata recovery
├── mocks/s3-errors/            # bounded HTTP 500 fixture for the real AWS SDK
└── simulations/               # Postman wiring and historical evidence
```

Исходные 39 файлов содержат 116 функций `Test…` и четыре benchmark. Дополнительно есть три теста инфраструктуры запуска. Helpers и синтетические payloads остаются в соответствующих `_test.go`; временные файлы, registry и архивы создаются через `t.TempDir()`. Постоянных отдельных testdata-файлов пока нет. Не переносите runtime scratch в репозиторий.

Группа `api` включает component tests с настоящим локальным storage и `httptest`, а не только изолированные unit tests. Проверки целого серверного процесса относятся к integration/end-to-end и сохраняют существующий тег `integration`; отдельного фиктивного набора E2E не добавлено. Multipart проверяется только там, где реализован: во внутреннем S3 backend кластера, а не в клиентском API.

## Команды

Выполняйте из корня репозитория. Runner передаёт аргументы стандартному `go test`; без аргументов использует `./...`. Необязательный `--` перед аргументами поддерживается. Все относительные package paths и output paths разрешаются от корня проекта. Не переопределяйте `-overlay` и `-C`.

```sh
# Обычные unit/component/security/recovery/backup tests:
go run ./tests -count=1 -timeout=3m ./...
# HTTP API, buckets/objects, authentication/grants, download и listing:
go run ./tests -count=1 .
# Отдельные исходные пакеты:
go run ./tests -race ./internal/storage
go run ./tests -race ./internal/access ./internal/httpapi
go run ./tests -race ./internal/backup ./cmd/triple-s-backup
# Конкретная проверка или семейство проверок:
go run ./tests -run '^TestTransaction' -count=1 ./internal/storage
# Integration и process end-to-end, включая обычные тесты:
go run ./tests -race -tags=integration -count=1 -timeout=5m ./...
# Большой файл и concurrent HTTP:
go run ./tests -race -tags=stress -run '^TestStress' -count=1 -timeout=5m .
TRIPLE_S_TEST_LARGE_MIB=16 go run ./tests -tags=stress -run '^TestStress' -timeout=5m .
# Benchmarks отдельно, без Test-функций:
go run ./tests -run '^$' -bench . -benchmem ./internal/storage
# Покрытие и полный список тестов всех build tags:
go run ./tests -coverprofile=/tmp/triple-s.cover ./...
go tool cover -func=/tmp/triple-s.cover
go run ./tests -tags=integration,clusterintegration,stress -list '^(Test|Benchmark)' ./...
# Статический анализ с учётом перенесённых тестов:
go run ./tests vet ./...
go build ./...
gofmt -l tests
```

Для фильтрации компонента используется **исходный** import/package path (`./internal/storage`), не физический `_suites` path. Это сохраняет доступ к закрытым сущностям и исходную принадлежность coverage.

### PostgreSQL/S3 integration и полная матрица

Требуются изолированные loopback PostgreSQL и MinIO, `TRIPLE_S_TEST_DATABASE_URL` и `TRIPLE_S_TEST_S3_ENDPOINT`. Credentials fixture заданы в `_suites/cluster/cluster_integration_test.go`; используйте только этот тестовый стенд. Не подключайте production или реальные AWS credentials. Fixture создаёт и удаляет временные SQL schemas, пишет в тестовый S3 bucket.

```sh
go run ./tests -race -tags=clusterintegration -run '^TestCluster' -count=1 -timeout=5m .
# Все Test-функции, включая integration, cluster и stress:
go run ./tests -race -tags=integration,clusterintegration,stress -count=1 -timeout=5m ./...
```

Без обеих переменных cluster tests **пропускаются** — успешный exit code не доказывает проверку PostgreSQL/S3. Для отдельного fault-injection test также нужен `TRIPLE_S_TEST_S3_FAILURE_ENDPOINT`.

Для existing Postman fixture (CLI с поддержкой `simulation`, версия 1.70+):

```sh
postman simulation run tests/simulations/s3-failure.sim.yaml --port 19400 --output ndjson
```

Запустите simulation в отдельном терминале. В терминале тестов задайте адрес локального stand-in и оба PostgreSQL/MinIO endpoint:

```sh
export TRIPLE_S_TEST_S3_FAILURE_ENDPOINT=http://127.0.0.1:19400/s3
go run ./tests -race -tags=clusterintegration -run '^TestClusterS3FailureSimulation$' -v .
```

Остановите stand-in после проверки. Он возвращает XML 500 для PUT/POST, HEAD 200 только для setup, 501 для непокрытых операций. Он не реализует функциональный S3, IAM или SigV4; состояние приложения проверяет настоящий Go AWS SDK + PostgreSQL. Логи запросов не коммитьте. Полная матрица без skips требует и основных зависимостей, и failure endpoint.

## Добавление тестов и ограничения

1. Поместите новый `_test.go` в существующую группу `_suites/`, сохраните имя исходного Go-пакета. Helpers должны также иметь суффикс `_test.go`.
2. Для нового исходного пакета добавьте соответствие в `layout.json`. Одинаковые имена файлов, отображаемые в один пакет, запрещены.
3. Применяйте `integration`, `clusterintegration`, `stress` только для соответствующих зависимостей/нагрузки; обычные тесты не должны требовать AWS credentials.
4. Размещайте постоянные fixtures в `tests/`; если тест читает их во время работы, используйте путь к физическому `tests/` от корня, а не к виртуальному файлу. Overlay влияет на сборку, **не на runtime filesystem**. Рабочий каталог тестового бинарника — исходный пакет, а не `_suites`.
5. Runner откажется запускать неполную/неоднозначную раскладку: неизвестную или пустую группу, дублирование virtual path, symlink вместо suite либо забытый `_test.go` вне `tests/` в исходных каталогах проекта. Скрытые каталоги, `data`, `bin`, vendor и node_modules исключены из этой проверки.

Docker build исключает `tests/`: production-бинарники не зависят от этой инфраструктуры. CI запускает тесты через runner; отдельного Makefile в проекте нет. В GoLand/IDE прямой Run Test и автоматический discovery без overlay больше не являются способом полного запуска.

Подробности гарантий и существующая матрица сценариев: [docs/testing.md](../docs/testing.md), [horizontal-scaling.md](../docs/horizontal-scaling.md).

## Проверка переноса: 2026-11-01

На macOS arm64 с Go 1.25.12 выполнены build, vet с полным набором tags, обычные тесты с coverage, race integration/stress и полная race-матрица `integration,clusterintegration,stress`. Все 119 Test-функций (116 исходных и три проверки runner) прошли без skips. Все четыре benchmark выполнены с `-benchtime=1x`; это smoke-проверка запуска, не сравнительный замер производительности.

Полная матрица использовала отдельные PostgreSQL 14.15, MinIO `RELEASE.2025-06-13T11-33-47Z` и Postman CLI 1.75.0. Перенесённая simulation получила три PUT с ожидаемыми ответами 500; Go-тест подтвердил отсутствие опубликованного объекта и расхода квоты. Проверялись только синтетические данные и loopback endpoints; сервисы после проверки остановлены.

До/после совпали все 2077 исходных coverage-блоков; прежние 47 production Go-файлов не изменились. Все 39 исходных тестовых файлов сохранены; в одном обновлён только комментарий команды запуска. Новые тесты относятся к инфраструктуре overlay.

Удалённые GitHub Actions и Docker image build в этой проверке не запускались. В cluster CI остаётся прежний Quay image, для которого ранее наблюдался registry 401; это существующее ограничение стенда, не результат переноса. Compose использует отдельную source build-конфигурацию MinIO.
