// Package stats — счётчики трафика и подключений, которые демон отправляет на control-сервер.
//
// Только суммы и числа — ни адресов, ни идентификаторов клиентов: сервер (и админка) узнают «сколько»,
// но не «кто». Байты копятся как прирост с прошлого успешного heartbeat: отправили — вычли
// отправленное (Commit), heartbeat не прошёл — прирост едет со следующим, ничего не теряется.
//
// Все методы безопасны для nil-указателя: модуль, которому счётчики не передали (тесты, ручной
// запуск без публикации), просто ничего не считает.
package stats

import "sync/atomic"

// Counters разделяются между модулями мостика, звонков и публикатором.
type Counters struct {
	bridgeBytes   atomic.Int64 // прирост трафика мостика сообщений с прошлого отчёта
	callsBytes    atomic.Int64 // прирост трафика звонков (TURN) с прошлого отчёта
	bridgeClients atomic.Int64 // сейчас подключено к мостику (разных клиентов)
	callsActive   atomic.Int64 // сейчас идёт звонков через TURN (аллокаций)
	bridgeKnown   atomic.Bool  // статистика мостика вообще доступна (sing-box её отдаёт)
}

// New создаёт пустые счётчики.
func New() *Counters { return &Counters{} }

// Report — то, что уходит в heartbeat.
type Report struct {
	BridgeBytes   int64
	CallsBytes    int64
	BridgeClients int64
	CallsActive   int64
	BridgeKnown   bool
}

func (c *Counters) AddBridgeBytes(n int64) {
	if c != nil && n > 0 {
		c.bridgeBytes.Add(n)
	}
}

func (c *Counters) AddCallsBytes(n int64) {
	if c != nil && n > 0 {
		c.callsBytes.Add(n)
	}
}

func (c *Counters) SetBridgeClients(n int64) {
	if c != nil {
		c.bridgeClients.Store(n)
		c.bridgeKnown.Store(true)
	}
}

func (c *Counters) SetCallsActive(n int64) {
	if c != nil {
		c.callsActive.Store(n)
	}
}

// Snapshot — текущие значения для отчёта (прирост байтов при этом не сбрасывается).
func (c *Counters) Snapshot() Report {
	if c == nil {
		return Report{}
	}
	return Report{
		BridgeBytes:   c.bridgeBytes.Load(),
		CallsBytes:    c.callsBytes.Load(),
		BridgeClients: c.bridgeClients.Load(),
		CallsActive:   c.callsActive.Load(),
		BridgeKnown:   c.bridgeKnown.Load(),
	}
}

// Commit вычитает уже доставленный на сервер прирост. Вычитание, а не обнуление: байты, накопленные
// пока шёл запрос, остаются на следующий отчёт.
func (c *Counters) Commit(sent Report) {
	if c == nil {
		return
	}
	c.bridgeBytes.Add(-sent.BridgeBytes)
	c.callsBytes.Add(-sent.CallsBytes)
}
