package stream

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

var (
	txTime     = regexp.MustCompile(`^((?:[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](?:[.,][0-9]{1,9})?)(?:\s+(.*))?$`)
	txWorker   = regexp.MustCompile(`^(.+?) \(thread no:\s*([0-9]+), pid:\s*([0-9]+)\) :: \*\*\* (.+?) \*\*\*\s*(.*)$`)
	txPool     = regexp.MustCompile(`^\s*TX Thread Pool (.+):Thread:([0-9]+):Bucket:([0-9]+)\s*$`)
	txRange    = regexp.MustCompile(`^\s*([0-9:.]+)\s+->\s+([0-9:.]+)\s*$`)
	txRoot     = regexp.MustCompile(`^\s*TX (.+?) on (\S+) \((.*?)\) Server Group Id:\s*(.*?), Start Root \[id:([^\]]+)\]\s*$`)
	txResult   = regexp.MustCompile(`(?i)^\s*TX (.+?) on (\S+) \((.*?)\)\s+(Success|Failed|Failure|Error|Aborted|Timeout|Timed out) \[id:([^\]]+)\], Total duration:([0-9.]+)ms, waited:([0-9.]+)ms\s*$`)
	txStep     = regexp.MustCompile(`^\s+TX (.+?)\((.*) on (\S+)\) (.*?), waited:([0-9.]+)ms, duration:([0-9.]+)ms\s*$`)
	txCustomer = regexp.MustCompile(`^\s*Customer Id:([^()]+)\((.*)\)\s*$`)
)

func integer(v string) int64   { n, _ := strconv.ParseInt(v, 10, 64); return n }
func decimal(v string) float64 { n, _ := strconv.ParseFloat(v, 64); return n }

func parseTX(source, host, path, raw string, transaction bool) Event {
	e := Normalize(source, host, path, raw)
	lines := strings.Split(raw, "\n")
	fields := map[string]any{"parser": ParserTX}
	stamp := txTime.FindStringSubmatch(lines[0])
	if len(stamp) > 0 {
		e.Timestamp = stamp[1]
		fields["time"] = stamp[1]
		fields["timestamp_precision"] = "time-only"
		if worker := txWorker.FindStringSubmatch(stamp[2]); len(worker) > 0 {
			fields["kind"] = "worker"
			fields["worker_path"] = worker[1]
			parts := strings.SplitN(worker[1], "/", 3)
			fields["worker"] = parts[0]
			if len(parts) > 1 {
				fields["component"] = parts[1]
			}
			if len(parts) > 2 {
				fields["pool"] = parts[2]
			}
			fields["thread"] = integer(worker[2])
			fields["pid"] = integer(worker[3])
			fields["severity"] = worker[4]
			fields["message"] = worker[5]
			severity := strings.ToLower(worker[4])
			if severity == "debugging" {
				severity = "debug"
			}
			e.Level = normalizeLevel(severity)
			if len(lines) > 1 {
				body := strings.Join(lines[1:], "\n")
				var payload any
				if json.Unmarshal([]byte(body), &payload) == nil {
					fields["payload"] = payload
				} else {
					fields["continuation"] = body
					if strings.EqualFold(strings.TrimSpace(worker[5]), "Request Body:") {
						fields["payload_parse_error"] = true
						fields["partial"] = true
					}
				}
			} else if strings.EqualFold(strings.TrimSpace(worker[5]), "Request Body:") {
				fields["partial"] = true
			}
			e.Fields = fields
			return e
		}
		fields["message"] = stamp[2]
	}
	steps := []map[string]any{}
	complete := false
	for _, line := range lines {
		if m := txCustomer.FindStringSubmatch(line); len(m) > 0 {
			transaction = true
			fields["customer_id"] = strings.TrimSpace(m[1])
			details := map[string]string{}
			for _, pair := range strings.Split(m[2], ",") {
				k, v, ok := strings.Cut(pair, ":")
				if ok {
					details[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
			fields["customer"] = details
		}
		if m := txRange.FindStringSubmatch(line); len(m) > 0 {
			fields["start_time"] = m[1]
			fields["end_time"] = m[2]
		}
		if m := txPool.FindStringSubmatch(line); len(m) > 0 {
			transaction = true
			fields["pool"] = m[1]
			fields["thread"] = integer(m[2])
			fields["bucket"] = integer(m[3])
		}
		if m := txRoot.FindStringSubmatch(line); len(m) > 0 {
			transaction = true
			setTXIdentity(fields, m[1], m[2], m[3], m[5])
			fields["server_group"] = m[4]
		}
		if m := txStep.FindStringSubmatch(line); len(m) > 0 {
			transaction = true
			steps = append(steps, map[string]any{"name": strings.TrimSpace(m[1]), "worker": m[2], "server": m[3], "status": m[4], "waited_ms": decimal(m[5]), "duration_ms": decimal(m[6])})
		}
		if m := txResult.FindStringSubmatch(line); len(m) > 0 {
			transaction = true
			complete = true
			setTXIdentity(fields, m[1], m[2], m[3], m[5])
			fields["status"] = strings.ToLower(m[4])
			fields["duration_ms"] = decimal(m[6])
			fields["waited_ms"] = decimal(m[7])
			fields["message"] = m[1] + " · " + m[4] + " · " + m[6] + " ms"
			e.Level = "info"
			if strings.ToLower(m[4]) != "success" {
				e.Level = "error"
			}
		}
	}
	if transaction {
		fields["kind"] = "transaction"
		fields["partial"] = !complete
		if len(steps) > 0 {
			fields["steps"] = steps
		}
		if !complete {
			e.Level = "info"
			fields["message"] = "TX transaction · partial record"
		}
		if e.Timestamp == "" {
			if end, ok := fields["end_time"].(string); ok {
				e.Timestamp = end
				fields["timestamp_precision"] = "time-only"
			}
		}
	} else if len(stamp) == 0 {
		// Unknown records retain the existing parser's JSON/text fields.
		return e
	} else {
		fields["kind"] = "message"
	}
	e.Fields = fields
	return e
}

func setTXIdentity(fields map[string]any, name, server, flow, id string) {
	fields["transaction"] = name
	fields["server"] = server
	fields["transaction_id"] = id
	parts := strings.SplitN(flow, ":", 3)
	if len(parts) >= 2 {
		fields["flow_id"] = parts[0]
		fields["flow_name"] = parts[1]
		if len(parts) == 3 {
			fields["flow_instance"] = parts[2]
		}
	} else {
		fields["flow"] = flow
	}
}
