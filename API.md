# Admin API Documentation

## Base URL

По умолчанию Admin API слушает:

```text
http://localhost:8080
```

Swagger UI:

```text
http://localhost:8080/swagger/index.html
```

Короткий редирект на Swagger UI:

```text
http://localhost:8080/docs
```

OpenAPI JSON:

```text
http://localhost:8080/swagger/doc.json
```

## Реализованные HTTP-ручки

Список ручек, которые фактически реализованы в проекте:

- `GET /health`
- `GET /docs`
- `GET /docs/`
- `GET /metrics`
- `GET /swagger/*`
- `GET /configs`
- `GET /configs/{namespace}/{key}`
- `PUT /configs/{namespace}/{key}`
- `DELETE /configs/{namespace}/{key}`
- `GET /features/{namespace}/{key}`
- `PUT /features/{namespace}/{key}`
- `DELETE /features/{namespace}/{key}`
- `GET /config`
- `POST /config/update`
- `POST /config/import`
- `GET /config/export`
- `GET /audit`
- `POST /cache/flush`

## Общая модель работы

Admin API хранит данные в Redis и использует namespace как изоляцию между сервисами. Для каждого namespace используются ключи:

```text
config:{namespace}:{key}
config_keys:{namespace}
feature:{namespace}:{key}
feature_keys:{namespace}
events:{namespace}
audit:{namespace}
```

Кратко по назначению:

- `config:{namespace}:{key}` — один config-параметр как Redis hash.
- `config_keys:{namespace}` — set-индекс всех config-ключей внутри namespace.
- `feature:{namespace}:{key}` — один feature toggle как Redis hash.
- `feature_keys:{namespace}` — set-индекс всех feature toggle ключей внутри namespace.
- `events:{namespace}` — Pub/Sub канал событий изменений для SDK.
- `audit:{namespace}` — list с историей изменений config-параметров.

Что происходит при изменениях:

1. API пишет config или feature toggle в Redis.
2. Для config API дополнительно ведет audit trail в `audit:{namespace}`.
3. API публикует событие в Redis Pub/Sub канал `events:{namespace}`.
4. SDK, подписанный на namespace, получает событие и обновляет локальный кэш без перезапуска приложения.

Общая особенность декодирования body:

- если `Content-Type` содержит `yaml` или `yml`, handlers декодируют YAML;
- во всех остальных случаях body читается как JSON;
- в примерах ниже используется JSON, потому что он лучше поддерживается Swagger UI.

Важно:

- `namespace` не создается отдельно. Он появляется автоматически при первой записи config или feature toggle.
- `GET /configs` показывает только namespace, у которых есть хотя бы один config-ключ.
- Namespace, в котором есть только feature toggles и нет config-ключей, в `GET /configs` не появится.

## Авторизация

В Admin API есть две группы ручек:

1. Публичные per-key ручки без JWT:
   - `GET /configs/{namespace}/{key}`
   - `PUT /configs/{namespace}/{key}`
   - `DELETE /configs/{namespace}/{key}`
   - `GET /features/{namespace}/{key}`
   - `PUT /features/{namespace}/{key}`
   - `DELETE /features/{namespace}/{key}`
   - `GET /health`
   - `GET /metrics`
   - `GET /docs`
   - `GET /swagger/*`

2. Защищенные legacy и aggregate ручки с JWT:
   - `GET /configs`
   - `GET /config`
   - `POST /config/update`
   - `POST /config/import`
   - `GET /config/export`
   - `GET /audit`
   - `POST /cache/flush`

Роли:

- `reader`
- `editor`
- `owner`
- `admin`

Иерархия ролей:

```text
reader < editor < owner < admin
```

Пример генерации токена:

```bash
TOKEN="$(go run ./cmd/token -subject admin@example.com)"
```

По умолчанию `cmd/token` выдает роль `admin`.

Для защищенных ручек передавайте:

```bash
-H "Authorization: Bearer $TOKEN"
```

Особенность protected endpoints:

- если поле `updatedBy` не передано, API подставит `sub` из JWT;
- если `updatedBy` передано, оно должно совпадать с `sub` из JWT;
- иначе вернется `400 updatedBy must match token subject`.

Особенность public per-key endpoints:

- JWT не нужен;
- если `updatedBy` не передано, API подставит `"api"`.

## Ошибки

Типовые ошибки:

- `400 Bad Request`
  - отсутствует обязательный параметр;
  - неверный `type`;
  - `expectedVersion < 0`;
  - `updatedBy` не совпадает с `sub` токена;
  - неверный формат JSON/YAML;
  - невалидный `bool` / `int` / `float` / `json`.
- `401 Unauthorized`
  - отсутствует Bearer token;
  - токен невалиден.
- `403 Forbidden`
  - у токена недостаточно прав.
- `404 Not Found`
  - ключ не найден.
- `409 Conflict`
  - конфликт версий.
- `500 Internal Server Error`
  - ошибка Redis или другая внутренняя ошибка.

Пример конфликта версий:

```text
version conflict for key "discount.percent": expected=1 current=2
```

Версионирование работает optimistic-lock способом:

- при создании нового значения текущая версия считается `0`;
- для успешного создания нужно передать `expectedVersion: 0`;
- при успешной записи версия увеличивается на `1`;
- если переданная версия не совпадает с текущей, запись не выполняется.

## Конфигурации

Поддерживаемые типы config-значений:

- `string`
- `bool`
- `int`
- `float`
- `json`

Все config-ручки работают в рамках одного `namespace`. Один и тот же `key` в разных namespace независим.

### Создать или обновить параметр

- Назначение: создать новый config-параметр или обновить существующий.
- Метод: `PUT`
- URL: `/configs/{namespace}/{key}`
- Авторизация: не требуется.

Path params:

- `namespace` - обязательный.
- `key` - обязательный.

Body:

- `value` - обязательный фактически, но поведение зависит от `type`:
  - для `string` пустая строка допустима;
  - для `bool`, `int`, `float`, `json` пустая строка приведет к `400`.
- `type` - обязательный.
- `expectedVersion` - необязательный технически, но практически обязателен для предсказуемых обновлений; если не передан, будет `0`.
- `isSecret` - необязательный, по умолчанию `false`.
- `updatedBy` - необязательный, по умолчанию `"api"`.

Пример запроса:

```bash
curl -X PUT "http://localhost:8080/configs/demo-service/discount.percent" \
  -H "Content-Type: application/json" \
  -d '{
    "value": "25",
    "type": "int",
    "expectedVersion": 0,
    "updatedBy": "admin@example.com"
  }'
```

Пример успешного ответа:

```json
{
  "namespace": "demo-service",
  "key": "discount.percent",
  "value": "25",
  "type": "int",
  "version": 1,
  "isSecret": false,
  "updatedAt": "2026-05-11T20:00:00Z",
  "updatedBy": "admin@example.com"
}
```

Возможные ошибки:

- `400 unsupported type "..."`.
- `400 invalid int value: ...`
- `400 invalid bool value: ...`
- `400 invalid float value: ...`
- `400 invalid json value: ...`
- `409 version conflict ...`

Что происходит внутри:

1. API валидирует `type`, `value` и `expectedVersion`.
2. В Redis через Lua script обновляется только один ключ.
3. Ключ добавляется в `config_keys:{namespace}`.
4. Для config записывается audit record в `audit:{namespace}`.
5. В канал `events:{namespace}` публикуется одно событие `operation=updated`, `resource=config`, `keys=["discount.percent"]`.
6. SDK получает событие и делает point reload только этого ключа.

### Получить параметр

- Назначение: получить один config-параметр.
- Метод: `GET`
- URL: `/configs/{namespace}/{key}`
- Авторизация: не требуется.

Пример:

```bash
curl "http://localhost:8080/configs/demo-service/discount.percent"
```

Пример успешного ответа:

```json
{
  "namespace": "demo-service",
  "key": "discount.percent",
  "value": "25",
  "type": "int",
  "version": 1,
  "isSecret": false,
  "updatedAt": "2026-05-11T20:00:00Z",
  "updatedBy": "admin@example.com"
}
```

Ошибки:

- `404 config item not found`
- `500 internal server error`

Что происходит внутри:

1. API читает hash `config:{namespace}:{key}` из Redis.
2. Никаких событий не публикуется.
3. SDK не меняет кэш.

### Удалить параметр

- Назначение: удалить один config-параметр.
- Метод: `DELETE`
- URL: `/configs/{namespace}/{key}`
- Авторизация: не требуется.

Query params:

- `updatedBy` - необязательный, если не передан, будет `"api"`.

Пример:

```bash
curl -X DELETE \
  "http://localhost:8080/configs/demo-service/app.title?updatedBy=admin@example.com"
```

Успешный ответ:

```text
204 No Content
```

Ошибки:

- `400 namespace is required`
- `400 key is required`
- `400 updatedBy is required`
- `404 config item not found`

Что происходит внутри:

1. API убеждается, что ключ существует.
2. Удаляет `config:{namespace}:{key}`.
3. Удаляет key из `config_keys:{namespace}`.
4. Пишет audit record с `result=deleted`.
5. Публикует одно событие `operation=deleted`, `resource=config`, `keys=["app.title"]`.
6. SDK удаляет ключ из локального кэша.

### Получить список параметров одного namespace

- Назначение: получить все config-параметры namespace.
- Метод: `GET`
- URL: `/config`
- Авторизация: Bearer token с ролью `reader` и выше.

Query params:

- `namespace` - обязательный.

Пример:

```bash
curl "http://localhost:8080/config?namespace=demo-service" \
  -H "Authorization: Bearer $TOKEN"
```

Пример успешного ответа:

```json
{
  "namespace": "demo-service",
  "items": [
    {
      "namespace": "demo-service",
      "key": "app.theme",
      "value": "dark",
      "type": "string",
      "version": 1,
      "isSecret": false,
      "updatedAt": "2026-05-11T20:01:00Z",
      "updatedBy": "admin@example.com"
    },
    {
      "namespace": "demo-service",
      "key": "discount.percent",
      "value": "25",
      "type": "int",
      "version": 1,
      "isSecret": false,
      "updatedAt": "2026-05-11T20:00:00Z",
      "updatedBy": "admin@example.com"
    }
  ]
}
```

Ошибки:

- `400 namespace is required`
- `401 missing bearer token`
- `403 forbidden`

Что происходит внутри:

1. API читает set `config_keys:{namespace}`.
2. Затем batched `HGETALL` читает все config hash-ключи namespace.
3. Элементы сортируются по `key`.
4. События не публикуются.

### Получить все config-namespace и все config-значения

- Назначение: получить все namespace, в которых есть config-ключи, и все config-параметры по ним.
- Метод: `GET`
- URL: `/configs`
- Авторизация: Bearer token с ролью `reader` и выше.

Пример:

```bash
curl "http://localhost:8080/configs" \
  -H "Authorization: Bearer $TOKEN"
```

Пример успешного ответа:

```json
{
  "namespaces": [
    "demo-service"
  ],
  "items": [
    {
      "namespace": "demo-service",
      "items": [
        {
          "namespace": "demo-service",
          "key": "app.theme",
          "value": "dark",
          "type": "string",
          "version": 1,
          "isSecret": false,
          "updatedAt": "2026-05-11T20:01:00Z",
          "updatedBy": "admin@example.com"
        },
        {
          "namespace": "demo-service",
          "key": "discount.percent",
          "value": "25",
          "type": "int",
          "version": 1,
          "isSecret": false,
          "updatedAt": "2026-05-11T20:00:00Z",
          "updatedBy": "admin@example.com"
        }
      ]
    }
  ]
}
```

Ограничение:

- ручка возвращает только config-данные;
- feature-only namespace сюда не попадут.

### Массовое обновление параметров

- Назначение: обновить или создать несколько config-параметров за один запрос.
- Метод: `POST`
- URL: `/config/update`
- Авторизация: Bearer token с ролью `editor` и выше.

Body:

- `namespace` - обязательный.
- `updatedBy` - необязательный, но если передан, должен совпадать с `sub` токена.
- `dryRun` - необязательный, по умолчанию `false`.
- `entries` - обязательный непустой массив.

Для каждого элемента `entries`:

- `key` - обязательный.
- `value` - обязательный фактически, но для `string` может быть пустым.
- `type` - обязательный, один из `string|bool|int|float|json`.
- `expectedVersion` - обязательный практически; если не передан, будет `0`.
- `isSecret` - необязательный, по умолчанию `false`.

Пример:

```bash
curl -X POST "http://localhost:8080/config/update" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "namespace": "demo-service",
    "updatedBy": "admin@example.com",
    "entries": [
      {
        "key": "app.title",
        "value": "Remote Config Demo",
        "type": "string",
        "expectedVersion": 0
      },
      {
        "key": "app.theme",
        "value": "dark",
        "type": "string",
        "expectedVersion": 0
      },
      {
        "key": "discount.percent",
        "value": "25",
        "type": "int",
        "expectedVersion": 0
      }
    ]
  }'
```

Пример успешного ответа:

```json
{
  "namespace": "demo-service",
  "dryRun": false,
  "items": [
    {
      "namespace": "demo-service",
      "key": "app.title",
      "value": "Remote Config Demo",
      "type": "string",
      "version": 1,
      "isSecret": false,
      "updatedAt": "2026-05-11T20:10:00Z",
      "updatedBy": "admin@example.com"
    },
    {
      "namespace": "demo-service",
      "key": "app.theme",
      "value": "dark",
      "type": "string",
      "version": 1,
      "isSecret": false,
      "updatedAt": "2026-05-11T20:10:00Z",
      "updatedBy": "admin@example.com"
    },
    {
      "namespace": "demo-service",
      "key": "discount.percent",
      "value": "25",
      "type": "int",
      "version": 1,
      "isSecret": false,
      "updatedAt": "2026-05-11T20:10:00Z",
      "updatedBy": "admin@example.com"
    }
  ]
}
```

Ошибки:

- `400 entries must not be empty`
- `400 duplicate key "..."` при дубликатах в `entries`
- `400 unsupported type "..."`.
- `409 version conflict ...`

Что происходит внутри:

1. API валидирует весь payload.
2. В одном Redis Lua script проверяются версии всех элементов.
3. Если конфликт или ошибка есть хотя бы у одного элемента, запись не выполняется ни для одного элемента.
4. При успехе создаются или обновляются все указанные config-ключи.
5. Для каждого config пишется audit record.
6. Публикуется одно общее событие `operation=updated`, `resource=config`, `keys=[...]`.
7. SDK перечитывает только измененные config-ключи.

### Важно: поведение при обновлении

Для config update поведение зафиксировано так:

- операция является `merge`, а не `replace`;
- обновляются только переданные `entries`;
- config-ключи, отсутствующие в запросе, не удаляются и не обнуляются;
- при успешном изменении версия каждого измененного ключа увеличивается на `1`;
- если ключа еще нет и `expectedVersion=0`, будет создание;
- если ключа еще нет и `expectedVersion != 0`, будет `409`;
- если `value` пустой и `type=string`, пустая строка будет сохранена;
- если `value` пустой и `type` не `string`, будет `400`;
- если `type` неизвестен, будет `400`;
- публикуется одно событие Redis Pub/Sub на весь запрос;
- SDK получает список измененных ключей и делает point reload только этих ключей;
- при `dryRun=true` данные, audit и события не создаются.

### Импорт конфигураций

- Назначение: импортировать несколько config-параметров одним запросом.
- Метод: `POST`
- URL: `/config/import`
- Авторизация: Bearer token с ролью `owner` и выше.

Поддерживаемые форматы тела:

- JSON
- YAML

Поддерживаются две формы payload:

1. `entries` - массив `ConfigUpdateEntry`, близкий к `/config/update`.
2. `items` - map по ключам, более удобный для bulk import и type inference.

Пример JSON import через `items`:

```bash
curl -X POST "http://localhost:8080/config/import" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "namespace": "demo-service",
    "updatedBy": "admin@example.com",
    "items": {
      "app.title": {
        "value": "Remote Config Demo",
        "type": "string",
        "expectedVersion": 0
      },
      "app.theme": "dark",
      "discount.percent": 25
    }
  }'
```

Пример YAML import:

```bash
curl -X POST "http://localhost:8080/config/import" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/x-yaml" \
  --data-binary @- <<'YAML'
namespace: demo-service
updatedBy: admin@example.com
items:
  app.title:
    value: Remote Config Demo
    type: string
    expectedVersion: 0
  app.theme: dark
  discount.percent: 25
YAML
```

Пример успешного ответа:

```json
{
  "namespace": "demo-service",
  "dryRun": false,
  "items": [
    {
      "namespace": "demo-service",
      "key": "app.theme",
      "value": "dark",
      "type": "string",
      "version": 1,
      "isSecret": false,
      "updatedAt": "2026-05-11T20:20:00Z",
      "updatedBy": "admin@example.com"
    },
    {
      "namespace": "demo-service",
      "key": "discount.percent",
      "value": "25",
      "type": "int",
      "version": 1,
      "isSecret": false,
      "updatedAt": "2026-05-11T20:20:00Z",
      "updatedBy": "admin@example.com"
    }
  ]
}
```

Фактическое поведение:

- импортирует только config-значения;
- может импортировать несколько значений сразу;
- создает новые записи и обновляет существующие;
- существующие значения, которых нет в import payload, не удаляются;
- ошибки в одном элементе роняют весь запрос;
- операция атомарна на весь запрос;
- при успешном import публикуется одно общее Pub/Sub событие на namespace;
- SDK обновляет локальный кэш через это событие;
- версия каждого измененного параметра увеличивается на `1`;
- явного лимита на размер payload в коде нет; действует фактический лимит по памяти/HTTP/Redis.

Поведение duplicate keys:

- в формате `entries` дубликаты ключей запрещены и приводят к `400 duplicate key`.
- в формате `items` ключи приходят как object/map. Отдельной проверки повторяющихся object keys после декодирования нет. Если исходный JSON/YAML повторяет один и тот же key несколько раз, фактическое поведение зависит от декодера и должно считаться неоднозначным. Это место лучше зафиксировать отдельно, если важно строгое поведение на дубликатах в raw object payload.

### Важно: поведение при импорте

Импорт работает как `merge`: обновляются только переданные параметры, отсутствующие в запросе существующие параметры не удаляются.

Дополнительно:

- import не является полной заменой namespace;
- import не затирает параметры, которых нет в запросе;
- import использует тот же optimistic locking, что и `/config/update`;
- import публикует одно событие Redis Pub/Sub с массивом измененных ключей;
- SDK получает событие и перечитывает только измененные config-ключи;
- при `dryRun=true` ничего не сохраняется и событие не публикуется;
- формат `items` умеет:
  - выводить `type` автоматически для `string`, `bool`, `int`, `float`, `json`;
  - принимать `isSecret`;
  - принимать `expectedVersion`;
  - собирать JSON-объект в строковое значение типа `json`;
  - отклонять `null`.

### Экспорт конфигураций

- Назначение: экспортировать config-параметры одного namespace.
- Метод: `GET`
- URL: `/config/export`
- Авторизация: Bearer token с ролью `reader` и выше.

Query params:

- `namespace` - обязательный.
- `format` - необязательный, `json` по умолчанию, поддерживаются `json` и `yaml`.

Пример JSON export:

```bash
curl "http://localhost:8080/config/export?namespace=demo-service&format=json" \
  -H "Authorization: Bearer $TOKEN"
```

Пример YAML export:

```bash
curl "http://localhost:8080/config/export?namespace=demo-service&format=yaml" \
  -H "Authorization: Bearer $TOKEN"
```

Пример успешного JSON-ответа:

```json
{
  "namespace": "demo-service",
  "items": [
    {
      "namespace": "demo-service",
      "key": "app.title",
      "value": "Remote Config Demo",
      "type": "string",
      "version": 1,
      "isSecret": false,
      "updatedAt": "2026-05-11T20:30:00Z",
      "updatedBy": "admin@example.com"
    },
    {
      "namespace": "demo-service",
      "key": "api.token",
      "value": "****",
      "type": "string",
      "version": 1,
      "isSecret": true,
      "updatedAt": "2026-05-11T20:30:00Z",
      "updatedBy": "admin@example.com"
    }
  ]
}
```

Фактическое поведение:

- экспортируются только config-значения;
- экспорт работает по одному namespace;
- feature toggles отдельно экспортировать нельзя, ручка не реализована;
- экспорт секретов не отдает исходное значение, а маскирует его как `****`;
- `isSecret=true` сохраняется в ответе;
- экспорт не подходит для lossless backup/import секретов;
- export response не совпадает по формату с import request и требует преобразования перед повторным импортом.

## Feature toggles

Feature toggle живет в namespace отдельно от config-ключей и хранится в Redis как:

```text
feature:{namespace}:{key}
```

### Создать или обновить feature toggle

- Назначение: создать новый toggle или переключить существующий.
- Метод: `PUT`
- URL: `/features/{namespace}/{key}`
- Авторизация: не требуется.

Body:

- `enabled` - логически обязательный. Важно: если поле не передано, Go-декодер оставит `false`, и API запишет `false`.
- `expectedVersion` - необязательный технически, но практически обязателен; если не передан, будет `0`.
- `updatedBy` - необязательный, по умолчанию `"api"`.

Пример включения фичи:

```bash
curl -X PUT "http://localhost:8080/features/demo-service/checkout_enabled" \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "expectedVersion": 0,
    "updatedBy": "admin@example.com"
  }'
```

Пример успешного ответа:

```json
{
  "namespace": "demo-service",
  "key": "checkout_enabled",
  "enabled": true,
  "version": 1,
  "updatedAt": "2026-05-11T20:40:00Z",
  "updatedBy": "admin@example.com"
}
```

Выключение делается тем же endpoint с `enabled: false`.

Что происходит внутри:

1. API валидирует `namespace`, `key`, `updatedBy`, `expectedVersion`.
2. Redis Lua script создает или обновляет только один toggle.
3. Ключ добавляется в `feature_keys:{namespace}`.
4. Публикуется событие `operation=updated`, `resource=feature`, `keys=["checkout_enabled"]`.
5. SDK перечитывает только этот feature toggle.

### Получить feature toggle

- Назначение: получить один toggle.
- Метод: `GET`
- URL: `/features/{namespace}/{key}`
- Авторизация: не требуется.

Пример:

```bash
curl "http://localhost:8080/features/demo-service/checkout_enabled"
```

Пример успешного ответа:

```json
{
  "namespace": "demo-service",
  "key": "checkout_enabled",
  "enabled": true,
  "version": 1,
  "updatedAt": "2026-05-11T20:40:00Z",
  "updatedBy": "admin@example.com"
}
```

Ошибки:

- `404 config item not found`
- `500 internal server error`

Примечание: текст `config item not found` используется и для feature toggles, потому что в текущей реализации используется общий `ErrNotFound`.

### Удалить feature toggle

- Назначение: удалить один toggle.
- Метод: `DELETE`
- URL: `/features/{namespace}/{key}`
- Авторизация: не требуется.

Query params:

- `updatedBy` - необязательный, по умолчанию `"api"`.

Пример:

```bash
curl -X DELETE \
  "http://localhost:8080/features/demo-service/new_banner?updatedBy=admin@example.com"
```

Успешный ответ:

```text
204 No Content
```

Что происходит внутри:

1. API проверяет, что toggle существует.
2. Удаляет `feature:{namespace}:{key}`.
3. Удаляет key из `feature_keys:{namespace}`.
4. Публикует событие `operation=deleted`, `resource=feature`, `keys=["new_banner"]`.
5. SDK удаляет toggle из локального кэша.

### Получить список feature toggles

На текущий момент ручка не реализована.

В коде есть storage/service методы `GetNamespace`, но HTTP endpoint для списка feature toggles отсутствует.

### Импорт feature toggles

На текущий момент ручка не реализована.

### Экспорт feature toggles

На текущий момент ручка не реализована.

### Важно: поведение при обновлении

Для feature toggle update поведение зафиксировано так:

- обновляется только один переданный feature toggle;
- это не операция над всем набором toggles;
- toggles, которых нет в запросе, не затрагиваются;
- если toggle не существовал и `expectedVersion=0`, он создается;
- если toggle не существовал и `expectedVersion != 0`, будет `409`;
- если поле `enabled` не передано, в текущей реализации будет записано `false`;
- при успешной записи версия увеличивается на `1`;
- публикуется одно событие Redis Pub/Sub на один toggle;
- SDK читает feature через `IsFeatureEnabled(key)`, которое возвращает `true` только если toggle есть в кэше и `enabled=true`.

## Health-check

### Проверка готовности сервиса

- Метод: `GET`
- URL: `/health`
- Авторизация: не требуется.

Пример:

```bash
curl "http://localhost:8080/health"
```

Успешный ответ:

```json
{
  "status": "ok",
  "redis": "up"
}
```

Если Redis недоступен:

```json
{
  "status": "degraded",
  "redis": "down",
  "error": "dial tcp 127.0.0.1:6379: connect: connection refused"
}
```

Фактическое поведение:

- ручка проверяет не только HTTP-сервер;
- внутри вызывается `PING` в Redis;
- поэтому ее можно использовать как readiness check для Admin API + Redis connectivity;
- при проблемах с Redis возвращается `503 Service Unavailable`.

## Служебные ручки

### Метрики

- Метод: `GET`
- URL: `/metrics`
- Авторизация: не требуется.
- Назначение: отдать Prometheus metrics.

Пример:

```bash
curl "http://localhost:8080/metrics"
```

Ответ:

```text
remote_config_...
```

### Audit по namespace

- Метод: `GET`
- URL: `/audit`
- Авторизация: Bearer token с ролью `reader` и выше.

Query params:

- `namespace` - обязательный.

Пример:

```bash
curl "http://localhost:8080/audit?namespace=demo-service" \
  -H "Authorization: Bearer $TOKEN"
```

Пример успешного ответа:

```json
{
  "namespace": "demo-service",
  "items": [
    {
      "namespace": "demo-service",
      "key": "discount.percent",
      "oldValue": "",
      "newValue": "25",
      "type": "int",
      "version": 1,
      "isSecret": false,
      "updatedAt": "2026-05-11T20:50:00Z",
      "updatedBy": "admin@example.com",
      "requestId": "req-123",
      "result": "updated"
    }
  ]
}
```

Важно:

- audit ведется только для config-операций;
- feature toggles в audit не пишутся;
- секретные config-значения в audit маскируются как `****`.

### Flush namespace

- Метод: `POST`
- URL: `/cache/flush`
- Авторизация: Bearer token с ролью `owner` и выше.

Body:

- `namespace` - обязательный.
- `updatedBy` - необязательный, но если передан, должен совпадать с `sub` токена.

Пример:

```bash
curl -X POST "http://localhost:8080/cache/flush" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "namespace": "demo-service",
    "updatedBy": "admin@example.com"
  }'
```

Пример успешного ответа:

```json
{
  "namespace": "demo-service",
  "status": "flush published"
}
```

Важно:

- ручка не меняет данные в Redis;
- она только публикует событие `operation=flush`;
- SDK после такого события делает полный reload namespace, а не point reload.

### Swagger и документация

- `GET /docs` - `307` redirect на `/swagger/index.html`
- `GET /docs/` - `307` redirect на `/swagger/index.html`
- `GET /swagger/index.html` - Swagger UI
- `GET /swagger/doc.json` - OpenAPI JSON

Эти ручки не меняют состояние системы.

## Как проверить hot-reload через API

1. Запустите Redis.
2. Запустите Admin API.
3. Запустите `demo-service` из `demo/`.
4. Откройте `http://localhost:8081`.
5. Создайте значения через Admin API:
   - `app.title`
   - `app.theme`
   - `discount.percent`
   - `new_banner`
   - `checkout_enabled`
6. Наблюдайте, как demo-service обновляет состояние без перезапуска.

Пояснение по SDK:

- config changes приходят как Pub/Sub событие `resource=config`, `operation=updated`, после чего SDK перечитывает только измененные config-ключи;
- feature changes приходят как `resource=feature`, после чего SDK перечитывает только измененные toggles;
- delete events удаляют ключ или toggle из локального кэша;
- flush event заставляет SDK перечитать весь namespace.

## Полный сценарий проверки через curl

```bash
TOKEN="$(go run ./cmd/token -subject admin@example.com)"

curl -X PUT "http://localhost:8080/configs/demo-service/app.title" \
  -H "Content-Type: application/json" \
  -d '{
    "value": "Remote Config Demo",
    "type": "string",
    "expectedVersion": 0,
    "updatedBy": "admin@example.com"
  }'

curl -X PUT "http://localhost:8080/configs/demo-service/app.theme" \
  -H "Content-Type: application/json" \
  -d '{
    "value": "dark",
    "type": "string",
    "expectedVersion": 0,
    "updatedBy": "admin@example.com"
  }'

curl -X PUT "http://localhost:8080/configs/demo-service/discount.percent" \
  -H "Content-Type: application/json" \
  -d '{
    "value": "25",
    "type": "int",
    "expectedVersion": 0,
    "updatedBy": "admin@example.com"
  }'

curl -X PUT "http://localhost:8080/features/demo-service/new_banner" \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "expectedVersion": 0,
    "updatedBy": "admin@example.com"
  }'

curl -X PUT "http://localhost:8080/features/demo-service/checkout_enabled" \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "expectedVersion": 0,
    "updatedBy": "admin@example.com"
  }'

curl "http://localhost:8080/config?namespace=demo-service" \
  -H "Authorization: Bearer $TOKEN"

curl "http://localhost:8080/configs" \
  -H "Authorization: Bearer $TOKEN"

curl "http://localhost:8080/features/demo-service/new_banner"

curl "http://localhost:8080/config/export?namespace=demo-service&format=json" \
  -H "Authorization: Bearer $TOKEN"

curl "http://localhost:8080/audit?namespace=demo-service" \
  -H "Authorization: Bearer $TOKEN"

curl -X POST "http://localhost:8080/cache/flush" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "namespace": "demo-service",
    "updatedBy": "admin@example.com"
  }'
```
