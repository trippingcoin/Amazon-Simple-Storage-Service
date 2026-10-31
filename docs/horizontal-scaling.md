# Горизонтальное масштабирование: PostgreSQL + S3 / MinIO

## Реализованный режим

`triple-s --backend cluster` запускает API без локального индекса объектов.
Любое число реплик использует одну PostgreSQL writer endpoint и один физический
S3 bucket. Логические бакеты и права находятся в PostgreSQL; S3 содержит только
неизменяемые blobs под уникальным префиксом кластера. Локальный режим остаётся
режимом по умолчанию и не меняет формат существующих данных.

```text
Clients -> Load balancer -> API replica 1 --+--> PostgreSQL primary / HA endpoint
                        -> API replica 2 --+--> S3 or distributed MinIO
                        -> API replica N --+
```

API-реплики не требуют sticky sessions, общей файловой системы или leader election.
Добавление реплики не умножает пользовательские квоты. PostgreSQL и S3 остаются
общими зависимостями: масштабирование API не превращает одиночную PostgreSQL или
одиночный MinIO в отказоустойчивый storage cluster.

## Контракт API и авторизация

Сохранены URL, XML, ETag/SHA-256, Range/conditional downloads, listing, namespace
изоляция и read/write grants. `X-Storage-User` сохраняет прежний смысл.

В cluster mode токены и grants загружаются из PostgreSQL **на каждом запросе**.
`sync-users` одной транзакцией заменяет реестр; последующие запросы на любой реплике
видят обновление без перезапуска. Принятые до отзыва запросы могут завершиться.
Недоступность авторизации возвращает 503; локального fallback токена нет.
`TRIPLE_S_TOKEN` и `--users-file` на API-реплике запрещены. В кластере обязательно
наличие активного admin. Удаление пользователя из реестра не удаляет его namespace.
Постоянные ID нельзя назначать другим людям.

`GET/HEAD /-/ready` — новый endpoint без токена, возвращающий только 200/503 и
проверяющий writer PostgreSQL, schema version и доступность физического S3 bucket.
Он не показывает конфигурацию или данные. Метрики остаются admin-only и собираются
**с каждой реплики**; их counters и admission limits локальны для процесса.

## Протокол записи и удаления

1. PUT запоминает generation логического бакета, потоково читает вход в приватный
   временный файл, проверяет размер, определяет MIME по содержимому и считает SHA-256.
   Предварительная проверка квоты отклоняет уже исчерпанный лимит до передачи в S3;
   окончательная проверка всё равно выполняется при публикации. В PostgreSQL
   регистрируется pending blob с новым случайным ключом.
2. SDK загружает файл в S3. От 8 MiB используется multipart с частями 8 MiB и двумя
   параллельными частями (SDK увеличивает размер частей при необходимости).
   Сетевой upload не удерживает SQL-транзакцию или namespace row lock.
3. Короткая SQL-транзакция блокирует строку namespace, проверяет generation бакета,
   состояние pending blob и квоты. Она одновременно публикует object metadata,
   обновляет usage, делает новый blob live и предыдущий retired. COMMIT — точка
   видимости объекта для всех реплик.
4. DELETE одной SQL-транзакцией убирает object metadata, уменьшает usage и помечает
   blob retired. Данные в S3 сразу не удаляются, поэтому открытое скачивание прежней
   версии не ломается от overwrite/delete на другой реплике.
5. Сбой до COMMIT сохраняет старую ссылку. Успешная загрузка, не дошедшая до публикации,
   остаётся в pending ledger для GC. При неизвестном результате COMMIT blob не
   удаляется: PostgreSQL мог уже сохранить ссылку.

Сериализуются только короткие изменения metadata **одного namespace**, а не весь
upload и не весь кластер. Это обеспечивает общие квоты, но один очень активный
namespace всё ещё может упираться в эту строку. Прирост throughput не измерялся.
Порядок overwrite — порядок SQL commit, без client idempotency keys или If-Match PUT.
Потерянный HTTP-ответ может соответствовать успешной операции; клиенту нужно
проверить состояние. Прокси не должен автоматически повторять записи.

Скачивание использует неизменяемый blob key. Seek преобразуется в новый S3 Range
GET без загрузки всего файла на API-диск. Время жизни reader ограничено 24 часами
и более коротким request context. Сам HTTP timeout по умолчанию — 2 минуты.

## Настройка

Зависимости закреплены в go.mod/go.sum: AWS SDK for Go v2, S3 transfermanager, pgx v5.
Для схемы используются возможности PostgreSQL 14+; локально проверено на 14,
CI-конфигурация использует 16.

| Переменная | Значение |
| --- | --- |
| TRIPLE_S_DATABASE_URL | PostgreSQL URI, production: `sslmode=verify-full` |
| TRIPLE_S_S3_BUCKET | Уже созданный приватный физический bucket |
| AWS_REGION | Регион физического bucket |
| TRIPLE_S_S3_ENDPOINT | Для MinIO; для Amazon S3 оставить пустым |
| TRIPLE_S_TEMP_DIR | Необязательный отдельный временный каталог API-реплики |
| TRIPLE_S_CLUSTER_INSECURE=1 | Только явно разрешённая dev-среда без TLS |

AWS credentials берутся из стандартной цепочки SDK: предпочтительны IAM роли;
для локального MinIO — AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY. Не передавайте
секреты аргументами команд и не сохраняйте их в Git. Endpoint/DSN с credentials
не выводятся приложением в ошибки HTTP или startup-логи.

Физические bucket, region и endpoint закрепляются при init. API отказывается
от запуска, если её конфигурация указывает другой storage. Не меняйте эти поля
SQL вручную у работающего кластера. DDL не запускается каждой API-репликой.

```sh
go build -o /tmp/triple-s .
go build -o /tmp/triple-s-cluster ./cmd/triple-s-cluster

# Сначала экспортируйте переменные из защищённой конфигурации.
# Формат users.json прежний; нужен хотя бы один активный admin.
/tmp/triple-s-cluster init --users-file /secure/users.json \
  --max-object-bytes 67108864 --max-storage-bytes 10737418240 --max-objects 100000

# На каждом узле, за TLS load balancer:
/tmp/triple-s --backend cluster --host 0.0.0.0 --allow-insecure-http

# Атомарное изменение токенов, disabled и grants для всех реплик:
/tmp/triple-s-cluster sync-users --users-file /secure/users.json

# Проверка готовности зависимостей:
/tmp/triple-s-cluster ready
```

`init` отказывается перезаписывать уже инициализированную БД. Default quotas из
init назначаются новым namespaces. `--max-object-bytes`, `--max-storage-bytes`,
`--max-objects` и `--dir` на API запрещены в cluster mode: лимиты не могут расходиться
между репликами. `--max-uploads` / `--max-downloads` ограничивают ресурсы одной API.
Квоты конкретного namespace оператор БД может изменить обычным UPDATE полей
`max_bytes`/`max_objects` в `ts_namespaces`; row lock согласует его с публикацией.
Не изменяйте счётчики `stored_bytes`/`object_count` вручную.

Размер пула — до 16 соединений PostgreSQL на реплику; SQL statement timeout 30s,
connect timeout 5s, SDK retry максимум 3 попытки. Планируйте общее число соединений
при увеличении replicas. Временные upload-файлы 0600 удаляются при завершении
запроса, но SIGKILL может оставить их до очистки ephemeral volume. Для Kubernetes
задайте отдельный emptyDir и ephemeral-storage limit; не используйте общий NFS
для scratch. Нужны дисковые ресурсы под concurrent uploads, не только RAM.

## Запуск локального стенда с несколькими репликами

`deploy/cluster/compose.yaml` — **локальный стенд**, не production HA-шаблон.
В нём одна PostgreSQL и один MinIO, зато API можно запускать несколькими экземплярами.
Dockerfile запускает приложение без root. Внешние порты стенда привязаны к loopback.
MinIO собирается из закреплённого официального source tag через
`deploy/cluster/minio.Dockerfile`: публичный Quay image для прежнего тега может
отвечать `401 Unauthorized`. Первая сборка поэтому дольше и требует доступа к
Docker Hub, Go module proxy и GitHub; последующие запуски используют Docker cache.

Задайте POSTGRES_PASSWORD (URL-safe, например случайный hex), MINIO_ROOT_USER,
MINIO_ROOT_PASSWORD и абсолютный TRIPLE_S_USERS_FILE с приватным users.json.
`export LOCAL_UID=$(id -u) LOCAL_GID=$(id -g)` позволяет admin-контейнеру прочитать
host-файл 0600. В production runtime не должен использовать MinIO root credentials.

```sh
docker compose -f deploy/cluster/compose.yaml up -d postgres minio
# Compose дождётся healthcheck PostgreSQL/MinIO перед admin-командами.
docker compose -f deploy/cluster/compose.yaml run --rm admin provision-local
docker compose -f deploy/cluster/compose.yaml run --rm admin init --users-file /run/users.json
docker compose -f deploy/cluster/compose.yaml up -d --scale api=3 api gateway
```

`provision-local` предназначен только для нового выделенного dev-bucket: он создаёт
bucket и устанавливает lifecycle для incomplete multipart, заменяя его dev lifecycle.
В production создание bucket, IAM и lifecycle делается инфраструктурным оператором.
NGINX использует Docker DNS для обнаружения реплик и не повторяет HTTP-операции
автоматически. Упавший процесс может дать одному запросу ошибку до обновления
DNS/пассивной проверки; это не обещание бесшовного завершения запросов на убитой реплике.

## Перенос текущих данных

Локальные данные не перемещаются автоматически при смене backend.

1. Остановите старый сервер и создайте проверенный local backup существующей утилитой.
2. Инициализируйте новый кластер с прежними постоянными user ID и нужными grants.
   ID отозванных пользователей также добавьте как disabled, если у них остались данные.
3. До открытия нового API клиентам выполните:

```sh
/tmp/triple-s-cluster import-local --dir /absolute/path/stopped-local-data
/tmp/triple-s-cluster verify
```

Импорт берёт файловые блокировки старого хранилища и копирует legacy namespace,
пользовательские пространства и пустые бакеты. Неизвестный owner ID останавливает
операцию вместо назначения данных другому пользователю. Целевые объекты не
перезаписываются: совпадающие SHA-256/размер пропускаются; различающиеся дают ошибку.
Прерванный импорт можно повторить. Частично перенесённые данные видны в новом
кластере, поэтому на время миграции остановите клиентские записи и не делайте cutover
до проверки. SHA-256 сохраняется, времена нормализуются до точности PostgreSQL
(микросекунды; HTTP Last-Modified по-прежнему секунды). Legacy-файлы доступны admin
без X-Storage-User. Старый каталог сохраняйте до подтверждения миграции.

`verify` полностью читает каждый текущий S3-объект, сравнивает SHA-256 и размеры
с metadata, проверяет namespace counters. Выполняйте при остановленных записях
и GC для стабильной проверки. Это аудит текущего кластера, **не создание backup**.

## Garbage collection и расходы

`triple-s-cluster gc --limit 100` удаляет batch pending/retired blobs старше 48 часов.
Он проверяет отсутствие SQL-ссылки и сначала фиксирует deleting-состояние в БД,
которое запрещает позднюю публикацию. Удаление идемпотентно и допускает повтор после
сбоя. Grace period превышает максимальную жизнь download reader. Не меняйте часы
БД/retention или записи ledger вручную.

GC не запускается автоматически: оператор вызывает его периодически своим
планировщиком. Пока GC не выполняется, retired/pending данные занимают место.
Даже с GC физический объём временно превышает логическую квоту. С versioning
DeleteObject может оставлять noncurrent versions: retention этих версий настраивается
отдельно и должен соответствовать backup-политике.

Для multipart после падения процесса нужен `AbortIncompleteMultipartUpload`
lifecycle, независимо от ledger GC. SDK пытается abort при обычной ошибке с отдельным
10-секундным timeout. [Назначение lifecycle](https://docs.aws.amazon.com/AmazonS3/latest/userguide/mpu-abort-incomplete-mpu-lifecycle-config.html).

## Безопасность и инфраструктура

- API не нуждается в DDL, изменении users/grants или S3 DeleteObject. Примеры
  ограничений: `deploy/cluster/database-roles.sql`, `iam-policy.example.json`.
  GC/operator используют отдельные credentials. Для SSE-KMS добавьте необходимые
  KMS-права с ограничением на используемый ключ.
- В production включите TLS для клиентов, PostgreSQL и S3/MinIO; Public Access Block,
  запрет публичных ACL, шифрование bucket. Не размещайте физический bucket публично.
- Используйте PostgreSQL HA writer endpoint и отказоустойчивый S3/распределённый MinIO.
  Автоматический failover самой БД/MinIO эта программа не создаёт.
- Метрики scrape каждой реплики, DB connection pool/lock waits, S3 latency/errors,
  scratch disk и размер pending/retired ledger требуют мониторинга.
- Нет автоматического SQL replay после неизвестного COMMIT. Для клиентского retry
  учитывайте возможность уже выполненной операции.

Row locks и их удержание до завершения транзакции описаны в
[документации PostgreSQL](https://www.postgresql.org/docs/current/explicit-locking.html).
Конфигурация динамического upstream основана на
[NGINX upstream](https://nginx.org/en/docs/http/ngx_http_upstream_module.html).

## Backup и восстановление кластера

Для просмотра реального кластера откройте `/-/observatory`. У существующей БД
однократно выполните `triple-s-cluster enable-observatory` от operator-роли;
это добавляет общий реестр heartbeat без повторного `init`.
[Доступ, обновление Compose и значения показателей](live-observatory.md).

`triple-s-backup` продолжает обслуживать **только локальный backend**. Копирование
диска API-реплики не сохраняет кластер. Нужны согласованные backup PostgreSQL и S3.
Простой baseline — остановить записи и GC, сделать pg_dump и независимую копию
физического S3-префикса, затем возобновить работу. Проверяйте восстановление в
отдельной БД и отдельном bucket командой `triple-s-cluster verify` и HTTP-тестами ACL.

Один pg_dump недостаточен: GC мог удалить blobs, на которые ссылается старый dump.
Один S3 versioning тоже не восстанавливает текущие keys под delete markers.
Для PITR нужны согласованные retention/версии и восстановление соответствующих
S3-версий либо независимая копия blobs. Не запускайте клонированную БД с GC против
живого production-префикса. При restore проверьте актуальные revoked/disabled/grants,
чтобы старый dump не восстановил отозванный доступ. AWS credentials и TLS-ключи
сохраняются отдельно; JSON registry/token hashes в БД — чувствительные данные.

## Проверка реализации

Локально проверены PostgreSQL 14.15 и MinIO RELEASE.2025-10-15T17-29-55Z:
две независимые connection pools, два процесса HTTP, остановка одной API-реплики,
общие квоты при concurrent uploads, read-only grants и отзыв без restart,
multipart и Range, незавершённая SQL-публикация, generation при delete/recreate
бакета, отложенный GC, import/retry/conflict и обнаружение отсутствующего blob.

Postman `s3-failure` запускает отдельный локальный HTTP fixture по S3 XML error
contract: реальный AWS SDK выполняет три PUT-попытки и не публикует metadata/usage
после 500. Это дополнение к реальному MinIO, не доказательство всех AWS failure modes.
AWS account / managed PostgreSQL failover не проверялись. Docker Engine на текущем
хосте не запущен: Compose прошёл config validation, но контейнерная сборка/топология
здесь не запускалась. Нагрузочный прирост от добавления реплик не измерялся.

```sh
TRIPLE_S_TEST_DATABASE_URL='postgres://triples_test@127.0.0.1:25432/postgres?sslmode=disable' \
TRIPLE_S_TEST_S3_ENDPOINT='http://127.0.0.1:19000' \
go run ./tests -race -tags=clusterintegration -run '^TestCluster' -v -count=1 -timeout=5m .
```

Тесты разрешают только loopback test endpoints и используют исключительно
фиктивные credentials. CI job `cluster` поднимает отдельные PostgreSQL/MinIO.
