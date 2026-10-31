# Phase 5: observability

## События и request ID

Приложение пишет JSON через стандартный `log/slog` в stderr. Уровень настраивается
`--log-level debug|info|warn|error`, по умолчанию `info`. Отдельного файла логов,
ротации, очереди или внешнего сервиса нет; сбор/ротация stderr остаются задачей
окружения. Запись синхронная: медленный сборщик логов может задерживать завершение
запросов. `warn` отключает события успешных запросов, но не меняет метрики.

Один `http_request` записывается при завершении запроса. Поля:
`request_id`, `operation`, `status`, `outcome`, `duration_seconds`,
`request_bytes` (прочитанное тело), `response_bytes` (записанное тело).
Начало/остановка процесса, deadline shutdown, cleanup и ошибки восстановления
имеют отдельные события. Помощь CLI и ошибки синтаксиса аргументов могут быть
обычным текстом. Ошибки транспорта обозначаются `http_transport_error`.

128-битный случайный ID возвращается в `X-Request-ID` и доступен сервисному коду
через `httpapi.RequestID(ctx)`. Входящий `X-Request-ID` не используется. Это
корреляция одного запроса, не распределённый trace и не идентификатор пользователя.

Логи не содержат bucket/key, URL/query, headers, IP, содержимое объектов, токены,
произвольные backend errors или panic payload. Это сознательно уменьшает детали
диагностики: событие 500 показывает операцию и ID, но не исходный текст ошибки.
Для поиска используйте ID из ответа клиента. Panic завершает соединение через
`http.ErrAbortHandler`, чтобы стандартный HTTP logger не вывел panic payload.
При обрыве после headers `status` может остаться 200, а `outcome` будет `aborted`
или `write_error`: статус сам по себе не доказывает успешную передачу.

## Метрики

`GET /-/metrics` возвращает Prometheus text 0.0.4; HEAD возвращает заголовки.
Endpoint защищён тем же Bearer-токеном, что и API. Путь не пересекается с валидным
бакетом (`-` недопустим). Новый read-only токен не добавлен: существующая модель
доверенных клиентов сохранена. Следовательно, мониторинг, получивший этот токен,
также может менять объекты; храните credential в доверенном окружении.

| Метрика | Значение |
|---|---|
| `storage_http_requests_total{operation,status,outcome}` | Завершённые HTTP-запросы API |
| `storage_operation_errors_total{operation,status,outcome}` | Запросы с HTTP >=400, отменой, обрывом, write error или panic |
| `storage_operation_duration_seconds{operation}` | Histogram длительности запроса, включая передачу тела |
| `storage_http_inflight_requests` | Активные запросы API |
| `storage_upload_bytes_total` | Прочитанные upload-байты, включая неуспешные попытки; не размер committed objects |
| `storage_download_bytes_total` | Записанные HTTP body bytes скачиваний 200/206, включая частичные передачи и multipart framing |

Имена `storage_*` отражают локальный backend вместо несуществующего AWS S3.
Аналог `s3_upload_duration_seconds` — histogram с `operation="upload"`,
для download — `operation="download"`. Это измерения на границе HTTP, а не
тайминги отдельных fsync/read/rename и не прямых вызовов repository.

`operation` имеет фиксированные значения: `upload`, `download`, `head`,
`metadata`, `create_bucket`, `delete_bucket`, `delete_object`, `list_buckets`,
`unauthorized`, `other`, а с Phase 6 — `list_objects`, `usage`. Значения outcome: `success`, `error`, `canceled`,
`write_error`, `aborted`, `panic`. Произвольных labels нет: имя файла и request ID
не увеличивают число временных рядов. Гистограмма использует границы
0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 30, 120 секунд и `+Inf`.

HEAD/304/XML не добавляют download bytes. Счётчик не подтверждает доставку
получателю: успешный Write означает передачу серверному HTTP transport.
Отклонённая до чтения загрузка добавляет ноль upload bytes. Операция записывается
после завершения handler; inflight показывает ещё работающие запросы.

Все метрики находятся в RAM отдельного экземпляра handler и сбрасываются при
перезапуске. Ряды operation появляются после первого события. Обращения к metrics
исключены из событий/счётчиков/гистограмм, кроме неавторизованных попыток.
Повторные попытки SDK не измеряются: SDK и автоматических retries нет;
повтор клиента считается новым HTTP-запросом.

## Подключение и запросы

[Пример конфигурации](../deploy/prometheus.example.yml) использует credential-файл,
содержащий токен без префикса `Bearer`. Не помещайте токен в репозиторий.
HTTP loopback подходит только для общего сетевого namespace. Для удалённого
сбора настройте HTTPS и проверку сертификата, адрес сервера и доступ к secret.
Пример не запускает Prometheus/Grafana и не меняет deployment.
Формат и поля сверены с официальными [exposition format](https://prometheus.io/docs/instrumenting/exposition_formats/)
и [configuration](https://prometheus.io/docs/prometheus/latest/configuration/configuration/).

Примеры PromQL (окно 5 минут):

```promql
# p95 upload/download, включая неуспешные попытки
histogram_quantile(0.95,
  sum by (le, operation) (
    rate(storage_operation_duration_seconds_bucket{operation=~"upload|download"}[5m])
  )
)

# Upload bytes/s
sum(rate(storage_upload_bytes_total[5m]))

# Ошибки по операциям, включая клиентские 4xx
sum by (operation) (rate(storage_operation_errors_total[5m]))

# Обрывы даже при уже отправленном статусе 200
sum(rate(storage_operation_errors_total{outcome=~"aborted|write_error|panic"}[5m]))
```

Для панели достаточно request rate, p95 transfer duration, errors, inflight и
bytes/s. Отдельно полезен `up` самого Prometheus для доступности endpoint.
Пороги алертов требуют рабочих SLO и baseline; готовые произвольные пороги
не включены. Отсутствие ряда ошибки до первой ошибки не следует трактовать как
поломку exporter. Недоступность scrape endpoint проверяется через `up`.

## Проверки и границы

Тесты проверяют JSON, контекст/ответ request ID, приватность, авторизацию metrics,
HTTP-контракт HEAD, отсутствие self-observation scrapes, inflight при параллельном
чтении, panic sanitization, write/stream failures и счётчики GET/Range/HEAD/304.
Полная матрица integration/stress с race detector прошла после добавления
middleware. Prometheus/Grafana серверы не разворачивались, `promtool` не запускался.
Новых внешних Go-зависимостей нет: небольшой exporter выдаёт только фиксированную
схему метрик и копирует snapshot под mutex перед сетевой записью.

OpenTelemetry/tracing, process/runtime collectors и внешний сборщик логов
не добавлены. Для текущего одного процесса достаточно request ID и метрик;
при появлении внешнего S3 backend или нескольких сервисов стоит добавить spans
на границе storage и метрики реальных SDK retries.

Пересоздание object metadata теперь выполняется при старте до HTTP-сервера
и не входит в request duration. См. [durable metadata](recoverable-metadata.md).

После включения многопользовательского доступа `/-/metrics` требует роль admin
(прежний TRIPLE_S_TOKEN также admin). Метрики общие для процесса и не содержат
user ID labels. `/-/usage` показывает только выбранное пространство пользователя.
