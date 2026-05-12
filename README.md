# Remote Config System

## API documentation

Подробное описание HTTP API находится в [API.md](./API.md).

`remote-config-system` — это MVP на Go для remote configuration и feature toggles с hot-reload.

В проекте есть два основных runtime-компонента:

- `cmd/admin-api` — HTTP API для управления конфигурациями и feature toggles. Он пишет данные в Redis и публикует события об изменениях.
- `pkg/sdk` — Go SDK, который загружает данные из Redis, хранит их в локальном потокобезопасном кэше, подписывается на Redis Pub/Sub и обновляет значения без перезапуска приложения.

Для отдельного демонстрационного SDK-потребителя и frontend см. [demo/README.md](demo/README.md).
Отдельная инструкция по полному запуску всей системы находится ниже в разделе `Полный запуск системы`.

Redis в этом MVP используется как единая общая зависимость для:

- хранения config-значений;
- хранения feature toggles;
- доставки событий обновления через Pub/Sub.

## Ключи Redis

Проект использует следующие шаблоны ключей Redis:

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

## Admin API

Основные endpoint'ы текущего MVP:

- `PUT /configs/{namespace}/{key}`
- `GET /configs/{namespace}/{key}`
- `DELETE /configs/{namespace}/{key}?updatedBy=...`
- `PUT /features/{namespace}/{key}`
- `GET /features/{namespace}/{key}`
- `DELETE /features/{namespace}/{key}?updatedBy=...`
- `GET /health`

Admin API использует JWT Bearer token и роли `reader`, `editor`, `owner`, `admin`. Legacy endpoint'ы (`/config/update`, `/config/import`, `/config/export`, `/audit`, `/cache/flush`) остались для совместимости с более ранней версией.

Клиенты больше не передают `expectedVersion`. Сервер сам вычисляет версию: при создании ставит `1`, при обновлении - `current+1`. Single-key обновления защищены per-resource Redis lock и при конкурентной записи возвращают `423 Locked`, а не `409`, потому что это временная блокировка ресурса. Bulk `/config/update` и `/config/import` работают как `merge only`, не удаляют неуказанные ключи и защищены namespace-level lock.

Lock keys и TTL:

- `config_write_lock:{namespace}:{key}` - single config write, TTL 30 секунд;
- `feature_write_lock:{namespace}:{key}` - single feature write, TTL 30 секунд;
- `config_bulk_lock:{namespace}` - bulk update/import, TTL 30 секунд.

### Пример запроса для config

```json
{
  "value": "15",
  "type": "int",
  "updatedBy": "admin@example.com"
}
```

### Пример запроса для feature toggle

```json
{
  "enabled": true,
  "updatedBy": "admin@example.com"
}
```

## Использование SDK

```go
package main

import (
	"context"
	"log"

	"github.com/1URose/remote-config-system/pkg/sdk"
)

func main() {
	client, err := sdk.NewClient(
		sdk.WithRedisAddr("localhost:6379"),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	payments := client.Namespace("payments")

	if err := client.Start(context.Background()); err != nil {
		log.Fatal(err)
	}

	timeout, _ := payments.GetInt("timeout")
	featureOn := payments.IsFeatureEnabled("new-checkout")

	log.Printf("timeout=%d feature=%v", timeout, featureOn)
}
```

Публичный MVP API SDK:

```go
func NewClient(options ...Option) (*Client, error)
func WithRedisAddr(addr string) Option
func WithRedisPassword(password string) Option
func WithRedisDB(db int) Option
func WithLogger(logger *slog.Logger) Option
func WithRetryInterval(interval time.Duration) Option

func (c *Client) Namespace(name string) *Namespace
func (c *Client) AttachNamespace(ctx context.Context, name string) (*Namespace, error)
func (c *Client) Start(ctx context.Context) error
func (c *Client) Close() error
func (c *Client) CacheSize() int
func (c *Client) RedisConnected() bool
func (c *Client) Stats() Stats

type Stats struct {
	Reloads        int64
	PointReloads   int64
	Flushes        int64
	Reconnects     int64
	RedisConnected bool
	CacheSize      int
}

func (ns *Namespace) Get(key string) (Value, bool)
func (ns *Namespace) GetRaw(key string) (Value, bool)
func (ns *Namespace) GetString(key string) (string, bool)
func (ns *Namespace) GetBool(key string) (bool, bool)
func (ns *Namespace) GetInt(key string) (int, bool)
func (ns *Namespace) GetFeature(key string) (Feature, bool)
func (ns *Namespace) IsFeatureEnabled(key string) bool

func (ns *Namespace) Watch(key string, cb func(Value))
func (ns *Namespace) WatchNamespace(cb func([]Value))
func (ns *Namespace) Reload() error
func (ns *Namespace) ReloadKeys(keys []string) error
func (ns *Namespace) Flush() error

func (v Value) Bool() (bool, error)
func (v Value) Int() (int, error)
```

## Полный запуск системы

В полном end-to-end сценарии участвуют три runtime-части:

1. `Redis` хранит config-значения, feature toggles и Pub/Sub события.
2. `Admin API` изменяет данные в Redis и публикует события обновления.
3. `Приложение-потребитель SDK` читает значения через `pkg/sdk` и получает обновления без перезапуска.

В этом репозитории готовым SDK-потребителем является demo-проект в `demo/`.

### Что и в какой последовательности запускать

1. Запустить Redis.
2. Запустить Admin API.
3. Запустить приложение, использующее SDK.
4. Если используется demo-проект, открыть demo-frontend в браузере.
5. Изменять значения через Admin API и проверять, что SDK-потребитель получает обновления без рестарта.

### Команды запуска

Из корня репозитория запустить Redis:

```bash
make docker-up
```

Если `make` недоступен:

```bash
docker compose up -d redis
```

Во втором терминале из корня репозитория запустить Admin API:

```bash
make run-admin
```

Если `make` недоступен:

```bash
go run ./cmd/admin-api
```

В третьем терминале запустить готовое demo-приложение на SDK:

```bash
cd demo
make run
```

Если `make` недоступен:

```bash
cd demo
go run ./cmd/demo-service
```

Открыть demo-frontend:

```text
http://localhost:8081
```

Проверить состояние demo API:

```bash
curl http://localhost:8081/api/state
```

### Как SDK используется в demo

`demo-service` не читает Redis напрямую.

Он использует SDK по стандартной схеме:

1. Создает клиент через `sdk.NewClient(...)`.
2. Регистрирует namespace через `client.Namespace("demo-service")`.
3. Один раз запускает SDK через `client.Start(ctx)`.
4. Читает значения только через публичные методы SDK.
5. Отдает эти значения через `GET /api/state`.

Цепочка обновления выглядит так:

```text
Admin API -> запись в Redis -> событие Redis Pub/Sub -> подписка внутри SDK -> обновление локального кэша SDK -> demo-service читает свежие значения из SDK -> frontend показывает новое состояние
```

### Как использовать SDK в своем сервисе

Если нужно использовать SDK вне demo-проекта:

1. Импортировать `github.com/1URose/remote-config-system/pkg/sdk`.
2. Создать один клиент для всех нужных namespace.
3. Зарегистрировать namespace через `client.Namespace(...)` до `Start(ctx)`.
4. Вызвать `Start(ctx)` при старте сервиса.
5. Читать значения через namespace-scoped API SDK.
6. Использовать в Admin API те же namespace, которые зарегистрированы в SDK-клиенте.

Минимальный пример:

```go
client, err := sdk.NewClient(
	sdk.WithRedisAddr("localhost:6379"),
)
if err != nil {
	log.Fatal(err)
}
defer client.Close()

app := client.Namespace("your-service")

if err := client.Start(context.Background()); err != nil {
	log.Fatal(err)
}

title, _ := app.GetString("app.title")
featureOn := app.IsFeatureEnabled("new-feature")
```

### Полный demo-сценарий

1. Запустить Redis.
2. Запустить Admin API.
3. Запустить `demo-service`.
4. Открыть `http://localhost:8081`.
5. Проверить стартовые значения.
6. Изменить `discount.percent` через Admin API.
7. Проверить, что скидка изменилась без перезапуска `demo-service`.
8. Изменить `app.theme` на `dark`.
9. Проверить, что тема изменилась сразу.
10. Выключить `new_banner`.
11. Проверить, что баннер исчез.
12. Включить `checkout_enabled`.
13. Проверить, что появилась кнопка нового checkout.

## Локальный запуск

Запустить Redis:

```bash
docker compose up -d redis
```

Запустить Admin API:

```bash
go run ./cmd/admin-api
```

Или через Make:

```bash
make run-admin
```

Проверка health:

```bash
curl http://localhost:8080/health
```

## Swagger

Swagger UI доступен по адресу:

```text
http://localhost:8080/swagger/index.html
```

Сгенерировать документацию:

```bash
make swagger-gen
```

Если генерация не видит DTO или `internal`-пакеты:

```bash
make swagger-gen-full
```

Установить `swag` CLI:

```bash
make swagger-install
```

Проверка:

```bash
make run-admin
```

После запуска Admin API открыть:

```text
http://localhost:8080/swagger/index.html
```

Через Swagger UI можно проверить:

- `PUT /configs/{namespace}/{key}`
- `GET /configs/{namespace}/{key}`
- `DELETE /configs/{namespace}/{key}`
- `PUT /features/{namespace}/{key}`
- `GET /features/{namespace}/{key}`
- `DELETE /features/{namespace}/{key}`
- `GET /health`
- `GET /config`
- `POST /config/update`
- `POST /config/import`
- `GET /config/export`
- `POST /cache/flush`
- `GET /audit`

## Ручной сценарий hot-reload

Сначала получите JWT для Admin API:

```bash
TOKEN="$(go run ./cmd/token -subject admin@example.com)"
```

1. Создать config-значение:

```bash
curl -X PUT http://localhost:8080/configs/payments/timeout \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"value":"15","type":"int","updatedBy":"admin@example.com"}'
```

2. Создать feature toggle:

```bash
curl -X PUT http://localhost:8080/features/payments/new-checkout \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"enabled":true,"updatedBy":"admin@example.com"}'
```

3. Запустить приложение, использующее SDK с namespace `payments`.

4. Обновить значение повторно:

```bash
curl -X PUT http://localhost:8080/configs/payments/timeout \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"value":"30","type":"int","updatedBy":"admin@example.com"}'
```

Поток обновления:

```text
Admin API -> запись в Redis -> событие Redis Pub/Sub -> подписка SDK -> обновление локального кэша -> приложение читает новое значение
```

Перезапуск приложения не требуется.

## Проверки для разработки

```bash
go mod tidy
go test ./...
go vet ./...
```

Если установлен `golangci-lint`:

```bash
golangci-lint run ./...
```

Доступные Make-команды:

```bash
make tidy
make test
make vet
make docker-up
make swagger-install
make swagger-gen
make swagger-gen-full
make run-admin
make run-demo
```
