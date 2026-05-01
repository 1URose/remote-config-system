# Remote Config System

Рабочий прототип удалённого управления конфигурациями и feature toggles на Go с Redis storage, hot-reload через Pub/Sub и локальным SDK cache.

## Архитектура

- `remote-config-api`:
  HTTP admin API, JWT auth, RBAC, валидация, optimistic locking, аудит, публикация update/flush событий в Redis.
- `remote-config-sdk`:
  библиотека для приложений, которая загружает namespace в локальный потокобезопасный cache, слушает Pub/Sub и дочитывает только изменённые ключи.
- `remoteconfig`:
  верхнеуровневый facade для использования как библиотеки: `remoteconfig.New(ctx, remoteconfig.Options{RedisAddr: ..., Namespaces: ...})`.
- `Redis`:
  хранит конфиги, версии, namespace key index, audit trail и Pub/Sub события.
- `examples/app`:
  консольное приложение со встроенным SDK, которое показывает hot-reload без рестарта.

Redis key layout:

- `cfg:{namespace}:{key}`: hash c value/type/version/is_secret/updated_at/updated_by
- `cfgkeys:{namespace}`: set ключей namespace
- `cfgupdates:{namespace}`: Pub/Sub канал обновлений
- `cfgaudit:{namespace}`: list audit records

## Структура репозитория

```text
.
├── docker-compose.yml
├── README.md
├── Dockerfile
├── go.mod
├── remote-config-api
├── remote-config-sdk
└── examples
    └── app
```

## Что реализовано

- Redis storage для config items и audit
- atomic batch update через Redis Lua script
- optimistic locking по `expectedVersion`
- hot-reload SDK через Redis Pub/Sub
- point reload только изменённых ключей
- SDK `Watch`, `WatchNamespace`, `Reload`, `ReloadKeys`, `Flush`
- last-known-good cache при временной недоступности Redis
- admin API: `health`, `config`, `config/update`, `config/import`, `config/export`, `cache/flush`, `audit`
- JWT auth и RBAC (`reader`, `editor`, `owner`, `admin`)
- dry-run для update/import
- mask secret values в audit/export
- structured logs и Prometheus-compatible `/metrics`
- unit/integration tests для ключевых сценариев

## Быстрый запуск

Нужны Docker и Docker Compose.

```bash
docker compose up --build -d redis api
```

Проверить health:

```bash
curl http://localhost:8080/health
```

## JWT и RBAC

Сгенерировать admin token:

```bash
docker compose run --rm --entrypoint /usr/local/bin/remote-config-token api \
  -subject admin@example.com \
  -roles admin
```

Сгенерировать reader token:

```bash
docker compose run --rm --entrypoint /usr/local/bin/remote-config-token api \
  -subject reader@example.com \
  -roles reader
```

Сохраняем токен:

```bash
export TOKEN="<paste token here>"
```

Проверка RBAC:

```bash
curl -i -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8080/config?namespace=payments"
```

Reader может читать, но не сможет вызвать `POST /config/update`.

## Swagger UI

После запуска открой:

```text
http://localhost:8080/docs
```

Там можно подставить JWT через `Authorize` и вызывать ручки прямо из браузера. Контракт отдается с:

```text
http://localhost:8080/openapi.yaml
```

## Ручной flow

1. Создать стартовый конфиг через admin API.

```bash
curl -X POST http://localhost:8080/config/update \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "namespace": "payments",
    "updatedBy": "admin@example.com",
    "dryRun": false,
    "entries": [
      {
        "key": "feature_x_enabled",
        "value": "true",
        "type": "bool",
        "expectedVersion": 0,
        "isSecret": false
      }
    ]
  }'
```

2. Запустить приложение.

```bash
docker compose up --build app
```

3. Изменить только один ключ и увидеть hot-reload без рестарта.

```bash
curl -X POST http://localhost:8080/config/update \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "namespace": "payments",
    "updatedBy": "admin@example.com",
    "dryRun": false,
    "entries": [
      {
        "key": "feature_x_enabled",
        "value": "false",
        "type": "bool",
        "expectedVersion": 1,
        "isSecret": false
      }
    ]
  }'
```

Приложение напечатает обновлённое значение и отработает `Watch`.

## Dry-run

```bash
curl -X POST http://localhost:8080/config/update \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "namespace": "payments",
    "updatedBy": "admin@example.com",
    "dryRun": true,
    "entries": [
      {
        "key": "feature_x_enabled",
        "value": "true",
        "type": "bool",
        "expectedVersion": 2,
        "isSecret": false
      }
    ]
  }'
```

Dry-run валидирует payload и version conflict, но не пишет в Redis и не публикует событие.

## Import / Export

JSON import использует тот же payload, что и update:

```bash
curl -X POST http://localhost:8080/config/import \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "namespace": "payments",
    "updatedBy": "admin@example.com",
    "dryRun": false,
    "entries": [
      {
        "key": "max_retries",
        "value": "3",
        "type": "int",
        "expectedVersion": 0,
        "isSecret": false
      }
    ]
  }'
```

YAML import:

```bash
curl -X POST http://localhost:8080/config/import \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/x-yaml" \
  --data-binary $'namespace: payments\nupdatedBy: admin@example.com\ndryRun: false\nentries:\n  - key: endpoint\n    value: https://api.example.com\n    type: string\n    expectedVersion: 0\n    isSecret: false\n'
```

Экспорт:

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8080/config/export?namespace=payments&format=json"
```

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8080/config/export?namespace=payments&format=yaml"
```

Секреты в export и audit маскируются.

## Audit

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8080/audit?namespace=payments"
```

## Cache Flush

Публикация namespace-level reload события для SDK:

```bash
curl -X POST http://localhost:8080/cache/flush \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "namespace": "payments",
    "updatedBy": "admin@example.com"
  }'
```

## Метрики

```bash
curl http://localhost:8080/metrics
```

Экспортируются counters/gauges:

- successful updates
- failed updates
- version conflicts
- flush requests
- reload requests
- Redis connection state
- average update apply duration

## Тесты

Если Go установлен локально:

```bash
go test ./...
```

## Примечания по fallback

- SDK продолжает обслуживать reads из памяти, если Redis временно недоступен после успешной загрузки.
- При восстановлении соединения SDK автоматически переподключается и делает namespace reload.
- При самом первом старте без Redis SDK не сможет загрузить initial state и вернёт ошибку: last-known-good существует только в памяти процесса.
