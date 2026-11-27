package agentbridge

import (
	"bufio"
	"io"
)

func bufioNewScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64<<10), 2<<20)
	return s
}
