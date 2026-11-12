package stream

import (
	"strings"
	"time"
)

const (
	ParserRegular  = "regular"
	ParserTX       = "tx"
	MaxRecordBytes = 32 * 1024
	maxRecordLines = 256
)

func ValidParser(mode string) bool { return mode == ParserRegular || mode == ParserTX }

// Parser belongs to one file session. Never share it between sources or reconnects.
// Regular mode keeps the existing one-line behavior. TX buffers bounded multiline
// records until a new header, closing separator, idle flush, or end of stream.
type Parser struct {
	mode, source, host, path string
	lines                    []string
	bytes                    int
	received                 time.Time
	transaction, open        bool
	truncated                bool
}

func NewParser(mode, source, host, path string) *Parser {
	return &Parser{mode: mode, source: source, host: host, path: path}
}

func (p *Parser) Push(line string) []Event {
	if p.mode != ParserTX {
		return []Event{Normalize(p.source, p.host, p.path, line)}
	}
	var out []Event
	flush := func() { out = append(out, p.Flush()...) }
	appendLine := func() {
		if p.bytes+len(line)+1 > MaxRecordBytes || len(p.lines) >= maxRecordLines {
			transaction, open := p.transaction, p.open
			part := p.Flush()
			for i := range part {
				if part[i].Fields == nil {
					part[i].Fields = map[string]any{}
				}
				part[i].Fields["partial"] = true
				part[i].Fields["record_limit_reached"] = true
			}
			out = append(out, part...)
			p.transaction, p.open = transaction, open
			p.truncated = true
		}
		p.append(line)
	}
	stamp := txTime.FindStringSubmatch(line)
	separator := isTXSeparator(line)
	if len(stamp) > 0 {
		flush()
		p.transaction = strings.TrimSpace(stamp[2]) == ""
	} else if separator {
		if len(p.lines) > 0 && !p.transaction {
			flush()
		}
		if p.transaction && p.open {
			appendLine()
			flush()
			return out
		}
		p.transaction, p.open = true, true
	} else if len(p.lines) == 0 && strings.HasPrefix(line, " ") {
		p.transaction = true // tail may start in the middle of a transaction.
	}
	if len(stamp) == 0 && !separator && len(p.lines) == 0 && !p.transaction {
		return append(out, Normalize(p.source, p.host, p.path, line))
	}
	appendLine()
	return out
}

func (p *Parser) append(line string) {
	if len(p.lines) == 0 {
		p.received = time.Now().UTC()
	}
	if len(line) > MaxRecordBytes-1 {
		line = line[:MaxRecordBytes-32] + " … [record truncated]"
		p.truncated = true
	}
	p.lines = append(p.lines, line)
	p.bytes += len(line) + 1
}

func (p *Parser) Flush() []Event {
	if len(p.lines) == 0 {
		return nil
	}
	lines, received, transaction, truncated := p.lines, p.received, p.transaction, p.truncated
	p.lines = nil
	p.bytes = 0
	p.transaction = false
	p.open = false
	p.truncated = false
	// A leading/trailing delimiter by itself is framing, not an event.
	content := false
	for _, line := range lines {
		if strings.TrimSpace(line) != "" && !isTXSeparator(line) {
			content = true
			break
		}
	}
	if !content {
		return nil
	}
	e := parseTX(p.source, p.host, p.path, strings.Join(lines, "\n"), transaction)
	if truncated {
		if e.Fields == nil {
			e.Fields = map[string]any{}
		}
		e.Fields["partial"] = true
		e.Fields["record_limit_reached"] = true
	}
	e.ReceivedAt = received
	return []Event{e}
}

func isTXSeparator(line string) bool {
	line = strings.TrimSpace(line)
	return len(line) >= 10 && strings.Trim(line, "-") == ""
}
