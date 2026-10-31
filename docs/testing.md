# Тестирование Triple-S

Все тесты, helpers и mocks физически находятся в корневой папке `tests/`.
[Структура, overlay runner и команды групп](../tests/README.md).
Существуют local и cluster backend: матрица ниже описывает локальные/HTTP сценарии,
а проверки PostgreSQL/MinIO, внутреннего multipart и ошибок SDK приведены в конце.
Внешние IAM/SigV4 и presigned API в Triple-S не реализованы.

## Матрица

| Область | Сценарии | Проверки |
|---|---|---|
| API | создание/список/удаление бакетов; PUT/GET/HEAD/DELETE; XML metadata; отсутствующие объекты; invalid paths | `main_test.go`, `download_test.go` |
| Авторизация | все методы и пути, неверная схема/токен, дублированный Authorization, auth перед Range/условными запросами, отсутствие утечки и мутации | `security_matrix_test.go`, `main_test.go` |
| Имена и filesystem | traversal, reserved keys, case aliases, symlinks, приватные права, permission denied | `storage_test.go`, `config_test.go` |
| Запись | лимит размера, неизвестная длина, обрыв, cancellation, отказ публикации, сохранение старой версии | `main_test.go`, `storage_test.go`, `stress_test.go` |
| Чтение | Range/suffix/multiple ranges, 416, ETag/304/412/If-Range, HEAD, закреплённая версия, cancellation, capacity, ошибка после headers | `download_test.go` в api, unit/storage и unit/httpapi |
| Конкурентность | параллельные операции/overwrite, цельная версия и checksum, лимит upload/download, busy Close | `storage_test.go`, `tests/_suites/unit/storage/download_test.go` |
| Recovery | restart, stale CSV, staging cleanup, crash до/после rename, lock, сохранение удалений | `storage_test.go`, `crash_test.go` |
| Небезопасное состояние | повреждённый JSON, неизвестный staging/root entry, symlinks, вложенные каталоги; отказ без удаления этих данных | `recovery_matrix_test.go` |
| Жизненный цикл HTTP | процесс сервера, timeout, SIGTERM с активной записью, restart, реальный GET/HEAD/Range | `integration_test.go` |
| TLS/disconnect | доверенный тестовый сертификат, обязательная auth, обрыв чтения и освобождение единственного download slot | `disconnect_integration_test.go` |
| Большой файл | 128 MiB chunked PUT, GET с проверкой SHA-256/ETag, oversize на 1 байт без потери старого объекта | `stress_test.go` |
| Нагрузка | 8 клиентов × 12 циклов PUT/GET/DELETE по 256 KiB, 288 запросов, checksum каждого чтения, пустой бакет в конце | `stress_test.go` |
| Backup/restore | реальный offline tar, новый каталог, байты/SHA-256/ETag/MIME/размер, пустые файлы/бакеты, Unicode, deleted object, права и последующая запись | `backup_test.go` |

В таблице указаны имена файлов: HTTP-тесты находятся в `tests/_suites/api`,
пакетные — в `tests/_suites/unit`, tagged suites — в `integration`, `cluster`, `stress`.
Тесты компилируются в контексте исходных пакетов через overlay; физически под `internal/` их нет.
Все данные временные, токен синтетический, AWS credentials не используются.
Тесты с сокетами слушают только loopback. `stress` вынесен из обычного запуска,
но обязателен в CI. Backup-тест требует доступного `tar` (есть на обоих CI runner).

## Запуск

```sh
go build ./...
go run ./tests vet ./...
go run ./tests -count=1 -timeout=3m ./...
go run ./tests -race -tags=integration -count=1 -timeout=5m ./...
go run ./tests -race -tags=stress -run '^TestStress' -count=1 -timeout=5m -v .
# Более крупный файл; допустимо 8..1024 MiB:
TRIPLE_S_TEST_LARGE_MIB=1024 go run ./tests -tags=stress -run '^TestStressLargeFile$' -timeout=5m -v .
# Выделения памяти и локальная скорость без HTTP:
go run ./tests ./internal/storage -run '^$' -bench 'Benchmark(PutObject|Download)$' -benchmem
```

Stress проверяет корректность под нагрузкой, а не production SLO. Ограничение
времени всего теста предотвращает зависание, но latency threshold намеренно
не задан: скорость shared CI runner нестабильна. Файл генерируется и проверяется
потоково; latency логируется, RSS не измеряется. Benchmem-замеры не заменяют измерение памяти процесса под длительной нагрузкой.

GitHub Actions `.github/workflows/ci.yml`: Linux/macOS, фиксированный Go 1.25.12,
раздельные build/vet/tests/race/stress шаги, минимальное read-only разрешение.
Workflow подготовлен локально; фактический запуск GitHub CI произойдёт после push.
Локальная проверка 26 октября 2026: вся матрица `integration,stress` с `-race`
прошла на macOS arm64; vet и сборки macOS/Linux прошли. Linux runtime проверит CI.

Локальный stress с race detector: 128 MiB upload ≈693 ms; download вместе с
проверкой ≈593 ms; 96 циклов при 8 клиентах ≈1,57 s, p50 цикла ≈130 ms,
p95 ≈161 ms. Эти числа относятся к одному запуску через loopback, не к сети
production и не доказывают предельную пропускную способность.

## Границы доказательств

Crash-тест завершает процесс, но не имитирует отключение питания/отказ диска.
Нет эмуляции реального ENOSPC, длительного soak-теста, сетевых разделений,
повреждения носителя и испытаний NFS. Безопасность TLS-конфигурации deployment
(сертификаты, proxy, rotation) требует отдельной проверки окружения.
Тест знает ожидаемые байты, но production recovery сам по себе не обнаружит
любое повреждение содержимого. Добавленные durable metadata также не являются
независимым immutable checksum manifest backup.
Для backup нужны внешняя проверка целостности, отдельная копия и периодический
restore drill; инструкция ниже не обещает online snapshot.

## Добавления Phase 6

`features_test.go`, `features_integration_test.go`, `tests/_suites/unit/storage/list_test.go` и
`tests/_suites/unit/storage/quota_test.go` покрывают пагинацию, usage, quotas, конкурентную публикацию
и восстановление счётчиков. Crash/backup тесты также проверяют usage. Детальная
семантика и результаты listing benchmark описаны в [Phase 6](additional-features.md).

## Regression: быстрые улучшения

`tests/_suites/unit/storage/regression_test.go`, `quick_fixes_test.go` и дополнения к
`tests/_suites/unit/httpapi/response_test.go` закрепляют:

- удаление первого/среднего/последнего объекта с legacy CSV, в том числе ключей
  с запятыми, кавычками и Unicode; сохранность остальных объектов после restart;
- отсутствие восстановления удалённых объектов/бакетов из устаревших CSV;
- одинаковое MIME-определение при upload и recovery: пустые/короткие файлы,
  Unicode, PNG/JPEG/PDF, бинарные данные, граница первых 512 байт;
- сохранение файлового offset при MIME detection и передачу ошибок чтения;
- отказ от nil upload body без panic, повреждения объекта и утечки upload slot;
- XML fallback 500 при ошибке сериализации, escaping и UTF-8 Content-Length;
- mapping обёрнутых quota/validation/timeout/body-limit/ENOSPC ошибок без
  раскрытия внутренних деталей;
- применение относительного `--dir` с пробелами/Unicode после смены cwd:
  upload, metadata, restart, download и delete используют выбранный каталог.

Рабочая реализация больше не редактирует CSV при delete: файлы — источник истины,
legacy CSV остаётся совместимым входом для необязательных timestamp бакетов.
Возвращать транзакции удаления строк CSV не требуется. MIME-код upload/recovery
объединён в `storage/content_type.go`. XML-ответы формируются общим HTTP helper;
его аварийный ответ теперь также XML, а не plain text. Существующий формат
успешных ответов сохранён для совместимости.

## Восстановимые metadata

`tests/_suites/unit/storage/object_metadata_test.go` проверяет reuse/repair, digest, identity,
потерю metadata, failed publish и небезопасные записи. `crash_test.go` расширен
точками отказа перед/после metadata rename и после blob unlink. Старые проверки
backup/recovery/concurrency продолжают выполняться с новым layout.
[Подробности и замеры](recoverable-metadata.md).

## Согласованная фиксация

`transaction_test.go`: PUT/DELETE crash matrix, idempotent replay, fail-stop,
reader visibility, cancellation, concurrent uploads, journal corruption и
backup незавершённого решения. Ошибка rename после решения теперь требует recovery;
проверки сохранения старой версии до решения используют pre-decision fault injection.

## Многопользовательская изоляция

`multiuser_test.go` проверяет межпользовательский доступ, административный scope,
отзыв/ротацию, конкурирующие записи, общий admission и offline backup/restore.
`internal/access` проверяет строгий private registry; `tests/_suites/unit/storage/tenants_test.go` —
изоляцию журналов при сбое и отказ на symlinks. `multiuser_integration_test.go`
проверяет реальный запуск/перезапуск с `--users-file`. Все входят в существующий CI.

`shared_access_test.go` проверяет read/write grants, доступ к соседним бакетам,
фильтрацию listing, запрет чужого usage, изменение только данных владельца,
restart и отзыв. Subprocess integration читает grants из файла и проверяет их
при реальном HTTP-доступе, включая запрет PUT для read-only получателя.

## Проверяемые backups

`internal/backup` проверяет конкретные архивы через recovery и полное чтение
объектов, а не только успешность tar. Тестируются corruption, missing manifest,
inventory mismatch, version rejection, path traversal/links/duplicates, отмена,
изоляция restore destination, live-lock rejection и все namespaces. CLI-тесты
проверяют команды и успешный JSON только после verify. Запуск:
`go run ./tests -race ./internal/backup ./cmd/triple-s-backup`.

## Несколько API-реплик

`cluster_integration_test.go` (tag `clusterintegration`) проверяет реальный
PostgreSQL/MinIO, две независимые реплики и два HTTP-процесса, остановку одного
процесса, distributed quota, ACL revocation, multipart/ranges, SQL publication
fault, bucket recreation, GC, import и verify. Дополнительный S3 500 fixture
находится в `tests/simulations/s3-failure.sim.yaml`. Команды и границы покрытия:
[horizontal-scaling.md](horizontal-scaling.md). CI содержит отдельный cluster job.
