# Demo Project

`demo/` — это полностью отдельный демонстрационный проект для `remote-config-system`.

Он показывает:

- hot-reload config-значений;
- обновление local cache внутри SDK;
- доставку событий через Redis Pub/Sub;
- работу feature toggles;
- изменение frontend без перезапуска `demo-service`.

Demo использует публичный SDK как внешний потребитель. Он не импортирует `internal` пакеты и не обращается к Redis напрямую.

Общий порядок запуска всей системы и роль SDK в полном сценарии описаны в корневом [README.md](../README.md), раздел `Полный запуск системы`.

## Структура

```text
demo/
├── cmd/
│   └── demo-service/
│       └── main.go
├── web/
│   └── index.html
├── README.md
└── Makefile
```

## Инструкция по запуску

Если нужно только быстро поднять demo и записать видео, достаточно этого раздела.

1. Запустить Redis из корня репозитория:

```bash
make docker-up
```

Если `make` недоступен:

```bash
docker compose up -d redis
```

2. В отдельном терминале запустить Admin API из корня репозитория:

```bash
make run-admin
```

Если `make` недоступен:

```bash
go run ./cmd/admin-api
```

3. В третьем терминале запустить demo-service:

```bash
cd demo
make run
```

Если `make` недоступен:

```bash
cd demo
go run ./cmd/demo-service
```

4. Открыть frontend:

```text
http://localhost:8081
```

5. Проверить, что `/api/state` возвращает текущее состояние:

```bash
curl http://localhost:8081/api/state
```

Ожидаемый стартовый ответ до изменений через Admin API:

```json
{
  "title": "Remote Config Demo",
  "theme": "light",
  "discount_percent": 10,
  "new_banner_enabled": true,
  "checkout_enabled": false
}
```

## Полный поток запуска demo

Из корня репозитория:

```bash
make docker-up
```

Во втором терминале:

```bash
make run-admin
```

В третьем терминале:

```bash
cd demo
make run
```

Открыть:

```text
http://localhost:8081
```

## Команды Admin API

Ниже используются актуальные текущие endpoint'ы Admin API:

- `PUT /configs/{namespace}/{key}`
- `PUT /features/{namespace}/{key}`

Эти примеры предполагают, что ключи еще не созданы, поэтому `expectedVersion` равен `0`. Если тот же ключ изменяется повторно, нужно передать актуальную версию.

Изменить заголовок:

```bash
curl -X PUT http://localhost:8080/configs/demo-service/app.title \
  -H "Content-Type: application/json" \
  -d '{
    "value": "Remote Config Demo v2",
    "type": "string",
    "expectedVersion": 0,
    "updatedBy": "demo-video"
  }'
```

Изменить скидку:

```bash
curl -X PUT http://localhost:8080/configs/demo-service/discount.percent \
  -H "Content-Type: application/json" \
  -d '{
    "value": "25",
    "type": "int",
    "expectedVersion": 0,
    "updatedBy": "demo-video"
  }'
```

Переключить тему:

```bash
curl -X PUT http://localhost:8080/configs/demo-service/app.theme \
  -H "Content-Type: application/json" \
  -d '{
    "value": "dark",
    "type": "string",
    "expectedVersion": 0,
    "updatedBy": "demo-video"
  }'
```

Выключить баннер:

```bash
curl -X PUT http://localhost:8080/features/demo-service/new_banner \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": false,
    "expectedVersion": 0,
    "updatedBy": "demo-video"
  }'
```

Включить новый checkout:

```bash
curl -X PUT http://localhost:8080/features/demo-service/checkout_enabled \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "expectedVersion": 0,
    "updatedBy": "demo-video"
  }'
```

## Сценарий для видео

1. Запустить Redis.
2. Запустить Admin API.
3. Запустить `demo-service`.
4. Открыть `http://localhost:8081`.
5. Показать стартовые значения по умолчанию.
6. Изменить `discount.percent` через Admin API.
7. Показать, что скидка изменилась без перезапуска `demo-service`.
8. Изменить `app.theme` на `dark`.
9. Показать, что тема изменилась сразу.
10. Выключить `new_banner`.
11. Показать, что баннер исчез.
12. Включить `checkout_enabled`.
13. Показать, что появилась кнопка нового checkout.

## Что именно доказывает demo

```text
Admin API изменяет значение
-> Redis сохраняет новое состояние
-> Redis Pub/Sub публикует событие
-> SDK внутри demo-service получает событие
-> SDK обновляет локальный кэш
-> frontend показывает новое состояние без перезапуска demo-service
```
