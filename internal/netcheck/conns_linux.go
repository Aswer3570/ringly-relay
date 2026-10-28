//go:build linux

package netcheck

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// EstablishedOn — сколько установленных TCP-соединений на локальном порту прямо сейчас.
// Только число: адреса клиентов мы не смотрим и не логируем — это IP пользователей.
func EstablishedOn(port int) (int, bool) {
	return countState(port, "01") // 01 = ESTABLISHED
}

func listeningInKernel(port int) (bool, bool) {
	n, ok := countState(port, "0A") // 0A = LISTEN
	return n > 0, ok
}

func countState(port int, state string) (int, bool) {
	want := fmt.Sprintf(":%04X", port)
	total, ok := 0, false
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		ok = true
		sc := bufio.NewScanner(f)
		sc.Scan() // заголовок
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			// local_address — поле 1, состояние — поле 3
			if len(fields) > 3 && strings.HasSuffix(fields[1], want) && fields[3] == state {
				total++
			}
		}
		f.Close()
	}
	return total, ok
}
