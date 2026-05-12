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

Публичные ручки без JWT:

- `GET /health`
- `GET /docs`
- `GET /swagger/*`

Остальные API-ручки требуют JWT Bearer token.

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

Чтобы вывести в консоль готовое значение с префиксом `Bearer` и скопировать его:

```bash
echo "Bearer $(go run ./cmd/token -subject admin@example.com)"
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

Матрица прав:

| Ручка | Минимальная роль |
|---|---|
| `GET /metrics` | `reader` |
| `GET /configs/{namespace}/{key}` | `reader` |
| `GET /features/{namespace}/{key}` | `reader` |
| `GET /configs` | `reader` |
| `GET /config` | `reader` |
| `GET /config/export` | `reader` |
| `GET /audit` | `reader` |
| `PUT /configs/{namespace}/{key}` | `editor` |
| `PUT /features/{namespace}/{key}` | `editor` |
| `POST /config/update` | `editor` |
| `DELETE /configs/{namespace}/{key}` | `owner` |
| `DELETE /features/{namespace}/{key}` | `owner` |
| `POST /config/import` | `owner` |
| `POST /cache/flush` | `owner` |

## Ошибки

Типовые ошибки:

- `400 Bad Request`
  - отсутствует обязательный параметр;
  - неверный `type`;
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
- `423 Locked`
  - config/feature resource уже заблокирован другой single-key write-операцией;
  - config namespace уже заблокирован bulk-операцией `/config/update` или `/config/import`;
  - выбран вместо `409`, потому что это временная блокировка ресурса, а не конфликт версии.
- `500 Internal Server Error`
  - ошибка Redis или другая внутренняя ошибка.

Версионирование вычисляется только сервером:

- клиент больше не передает `expectedVersion`;
- при создании нового значения версия становится `1`;
- при обновлении существующего значения версия становится `current + 1`;
- single-key ручки защищены per-resource Redis lock и при конкурентной записи возвращают `423 Locked`;
- bulk-ручки используют `merge only` и namespace-level lock.

Redis locks:

- `config_write_lock:{namespace}:{key}` - single-key config write, TTL 30 секунд;
- `feature_write_lock:{namespace}:{key}` - single-key feature write, TTL 30 секунд;
- `config_bulk_lock:{namespace}` - bulk `/config/update` и `/config/import`, TTL 30 секунд.

Все locks берутся через `SET NX` с TTL и освобождаются Lua script с проверкой token. Single config write также учитывает `config_bulk_lock:{namespace}` и возвращает `423 Locked`, если namespace занят bulk-операцией.

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
- Авторизация: Bearer token с ролью `editor` и выше.

Path params:

- `namespace` - обязательный.
- `key` - обязательный.

Body:

- `value` - обязательный фактически, но поведение зависит от `type`:
  - для `string` пустая строка допустима;
  - для `bool`, `int`, `float`, `json` пустая строка приведет к `400`.
- `type` - обязательный.
- `isSecret` - необязательный, по умолчанию `false`.
- `updatedBy` - необязательный, по умолчанию `"api"`.

`expectedVersion` в запросе не передается. Версия вычисляется сервером атомарно: новая запись получает `version=1`, существующая - `current+1`. Перед записью API проверяет `config_bulk_lock:{namespace}`, затем берет `config_write_lock:{namespace}:{key}`. Если namespace или key уже заблокирован другой write-операцией, запрос получает `423 Locked`, значение не меняется и Pub/Sub событие не публикуется.

Пример запроса:

```bash
curl -X PUT "http://localhost:8080/configs/demo-service/discount.percent" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "value": "25",
    "type": "int",
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
- `423 config "namespace"/"key" is locked by another write operation`
- `423 namespace "..." is locked by another write operation`

Что происходит внутри:

1. API валидирует `type` и `value`.
2. Проверяет `config_bulk_lock:{namespace}` и берет `config_write_lock:{namespace}:{key}` через `SET NX` с TTL 30 секунд.
3. В Redis через Lua script атомарно проверяет namespace lock, читает текущую версию и обновляет только один ключ.
4. Ключ добавляется в `config_keys:{namespace}`.
5. Для config записывается audit record в `audit:{namespace}`.
6. В канал `events:{namespace}` публикуется одно событие `operation=updated`, `resource=config`, `keys=["discount.percent"]`.
7. SDK получает событие и делает point reload только этого ключа.
8. Lock освобождается через Lua script с проверкой token, включая ошибочные завершения.

### Получить параметр

- Назначение: получить один config-параметр.
- Метод: `GET`
- URL: `/configs/{namespace}/{key}`
- Авторизация: Bearer token с ролью `reader` и выше.

Пример:

```bash
curl "http://localhost:8080/configs/demo-service/discount.percent" \
  -H "Authorization: Bearer $TOKEN"
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
- Авторизация: Bearer token с ролью `owner` и выше.

Query params:

- `updatedBy` - необязательный, если не передан, будет `"api"`.

Пример:

```bash
curl -X DELETE \
  "http://localhost:8080/configs/demo-service/app.title?updatedBy=admin@example.com" \
  -H "Authorization: Bearer $TOKEN"
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
- `isSecret` - необязательный, по умолчанию `false`.

`expectedVersion` в запросе не передается. Для каждого измененного ключа сервер сам вычисляет следующую версию.

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
        "type": "string"
      },
      {
        "key": "app.theme",
        "value": "dark",
        "type": "string"
      },
      {
        "key": "discount.percent",
        "value": "25",
        "type": "int"
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
- `423 namespace "..." is locked by another write operation`

Что происходит внутри:

1. API валидирует весь payload.
2. Redis lock `config_bulk_lock:{namespace}` берется через `SET NX` с TTL 30 секунд; если lock уже есть, API возвращает `423 Locked`.
3. В одном Redis Lua script вычисляются следующие версии всех элементов.
4. Если ошибка есть хотя бы у одного элемента, запись не выполняется ни для одного элемента.
5. При успехе создаются или обновляются все указанные config-ключи.
6. Для каждого config пишется audit record.
7. Публикуется одно общее событие `operation=updated`, `resource=config`, `keys=[...]`.
8. SDK перечитывает только измененные config-ключи.
9. Lock освобождается после завершения операции, включая ошибочные завершения; TTL остается страховкой от зависшего процесса.

### Важно: поведение при обновлении

Для config update поведение зафиксировано так:

- операция является `merge`, а не `replace`;
- обновляются только переданные `entries`;
- config-ключи, отсутствующие в запросе, не удаляются и не обнуляются;
- при успешном изменении версия каждого измененного ключа увеличивается на `1`;
- если ключа еще нет, будет создание с `version=1`;
- если ключ уже есть, будет обновление с `version=current+1`;
- если `value` пустой и `type=string`, пустая строка будет сохранена;
- если `value` пустой и `type` не `string`, будет `400`;
- если `type` неизвестен, будет `400`;
- публикуется одно событие Redis Pub/Sub на весь запрос;
- SDK получает список измененных ключей и делает point reload только этих ключей;
- при `dryRun=true` данные, audit и события не создаются;
- параллельные bulk-операции в одном namespace не выполняются одновременно: используется namespace-level lock.

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
        "type": "string"
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
- перед записью берется namespace-level lock, общий с `/config/update`;
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
- import использует тот же namespace-level lock, что и `/config/update`;
- import публикует одно событие Redis Pub/Sub с массивом измененных ключей;
- SDK получает событие и перечитывает только измененные config-ключи;
- при `dryRun=true` ничего не сохраняется и событие не публикуется;
- формат `items` умеет:
  - выводить `type` автоматически для `string`, `bool`, `int`, `float`, `json`;
  - принимать `isSecret`;
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
- Авторизация: Bearer token с ролью `editor` и выше.

Body:

- `enabled` - логически обязательный. Важно: если поле не передано, Go-декодер оставит `false`, и API запишет `false`.
- `updatedBy` - необязательный, по умолчанию `"api"`.

`expectedVersion` в запросе не передается. Версия вычисляется сервером атомарно: новая запись получает `version=1`, существующая - `current+1`. Перед записью API берет `feature_write_lock:{namespace}:{key}` через `SET NX` с TTL 30 секунд. Если toggle уже заблокирован другой write-операцией, запрос получает `423 Locked`, значение не меняется и Pub/Sub событие не публикуется.

Пример включения фичи:

```bash
curl -X PUT "http://localhost:8080/features/demo-service/checkout_enabled" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
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

1. API валидирует `namespace`, `key`, `updatedBy`.
2. Берет `feature_write_lock:{namespace}:{key}` через `SET NX` с TTL 30 секунд.
3. Redis Lua script атомарно читает текущую версию и создает или обновляет только один toggle.
4. Ключ добавляется в `feature_keys:{namespace}`.
5. Публикуется событие `operation=updated`, `resource=feature`, `keys=["checkout_enabled"]`.
6. SDK перечитывает только этот feature toggle.
7. Lock освобождается через Lua script с проверкой token, включая ошибочные завершения.

Возможные ошибки:

- `400 validation error`
- `423 feature "namespace"/"key" is locked by another write operation`
- `500 internal server error`

### Получить feature toggle

- Назначение: получить один toggle.
- Метод: `GET`
- URL: `/features/{namespace}/{key}`
- Авторизация: Bearer token с ролью `reader` и выше.

Пример:

```bash
curl "http://localhost:8080/features/demo-service/checkout_enabled" \
  -H "Authorization: Bearer $TOKEN"
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
- Авторизация: Bearer token с ролью `owner` и выше.

Query params:

- `updatedBy` - необязательный, по умолчанию `"api"`.

Пример:

```bash
curl -X DELETE \
  "http://localhost:8080/features/demo-service/new_banner?updatedBy=admin@example.com" \
  -H "Authorization: Bearer $TOKEN"
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
- если toggle не существовал, он создается с `version=1`;
- если toggle существовал, он обновляется с `version=current+1`;
- если поле `enabled` не передано, в текущей реализации будет записано `false`;
- при успешной записи версия увеличивается на `1`;
- публикуется одно событие Redis Pub/Sub на один toggle;
- SDK читает feature через namespace-scoped API, например `ns.IsFeatureEnabled(key)`, который возвращает `true` только если toggle есть в кэше и `enabled=true`.

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
- Авторизация: Bearer token с ролью `reader` и выше.
- Назначение: отдать Prometheus metrics.

Пример:

```bash
curl "http://localhost:8080/metrics" \
  -H "Authorization: Bearer $TOKEN"
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
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "value": "Remote Config Demo",
    "type": "string",
    "updatedBy": "admin@example.com"
  }'

curl -X PUT "http://localhost:8080/configs/demo-service/app.theme" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "value": "dark",
    "type": "string",
    "updatedBy": "admin@example.com"
  }'

curl -X PUT "http://localhost:8080/configs/demo-service/discount.percent" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "value": "25",
    "type": "int",
    "updatedBy": "admin@example.com"
  }'

curl -X PUT "http://localhost:8080/features/demo-service/new_banner" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "updatedBy": "admin@example.com"
  }'

curl -X PUT "http://localhost:8080/features/demo-service/checkout_enabled" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "updatedBy": "admin@example.com"
  }'

curl "http://localhost:8080/config?namespace=demo-service" \
  -H "Authorization: Bearer $TOKEN"

curl "http://localhost:8080/configs" \
  -H "Authorization: Bearer $TOKEN"

curl "http://localhost:8080/features/demo-service/new_banner" \
  -H "Authorization: Bearer $TOKEN"

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
