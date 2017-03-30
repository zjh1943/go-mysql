package canal

import (
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/juju/errors"
	"github.com/ngaut/log"
	"github.com/zjh1943/go-mysql/dump"
	"github.com/zjh1943/go-mysql/schema"
)

type dumpParseHandler struct {
	c    *Canal
	name string
	pos  uint64
}

func (h *dumpParseHandler) BinLog(name string, pos uint64) error {
	h.name = name
	h.pos = pos
	return nil
}

func (h *dumpParseHandler) Data(db string, table string, values []string) error {
	if h.c.isClosed() {
		return errCanalClosed
	}

	tableInfo, err := h.c.GetTable(db, table)
	if err != nil {
		log.Errorf("get %s.%s information err: %v", db, table, err)
		return errors.Trace(err)
	}

	vs := make([]interface{}, len(values))

	for i, v := range values {
		if v == "NULL" {
			vs[i] = nil
		} else if v[0] != '\'' {
			if tableInfo.Columns[i].Type == schema.TYPE_NUMBER {
				n, err := strconv.ParseInt(v, 10, 64)
				if err != nil {
					log.Errorf("parse row %v at %d error %v, skip", values, i, err)
					return dump.ErrSkip
				}
				vs[i] = n
			} else if tableInfo.Columns[i].Type == schema.TYPE_FLOAT {
				f, err := strconv.ParseFloat(v, 64)
				if err != nil {
					log.Errorf("parse row %v at %d error %v, skip", values, i, err)
					return dump.ErrSkip
				}
				vs[i] = f
			} else {
				log.Errorf("parse row %v error, invalid type at %d, skip", values, i)
				return dump.ErrSkip
			}
		} else {
			vs[i] = unescapeSqlString(v[1 : len(v)-1])
			// vs[i] = v[1 : len(v)-1]
		}
	}

	events := newRowsEvent(tableInfo, InsertAction, [][]interface{}{vs})
	return h.c.travelRowsEventHandler(events)
}

func (c *Canal) AddDumpDatabases(dbs ...string) {
	if c.dumper == nil {
		return
	}

	c.dumper.AddDatabases(dbs...)
}

func (c *Canal) AddDumpTables(db string, tables ...string) {
	if c.dumper == nil {
		return
	}

	c.dumper.AddTables(db, tables...)
}

func (c *Canal) AddDumpIgnoreTables(db string, tables ...string) {
	if c.dumper == nil {
		return
	}

	c.dumper.AddIgnoreTables(db, tables...)
}

func (c *Canal) tryDump() error {
	if len(c.master.Name) > 0 && c.master.Position > 0 {
		// we will sync with binlog name and position
		log.Infof("skip dump, use last binlog replication pos (%s, %d)", c.master.Name, c.master.Position)
		return nil
	}

	if c.dumper == nil {
		log.Info("skip dump, no mysqldump")
		return nil
	}

	h := &dumpParseHandler{c: c}

	start := time.Now()
	log.Info("try dump MySQL and parse")
	if err := c.dumper.DumpAndParse(h); err != nil {
		return errors.Trace(err)
	}

	log.Infof("dump MySQL and parse OK, use %0.2f seconds, start binlog replication at (%s, %d)",
		time.Now().Sub(start).Seconds(), h.name, h.pos)

	c.master.Update(h.name, uint32(h.pos))
	c.master.Save(true)

	return nil
}

func unescapeSqlString(s string) string {
	found := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			found = true
			break
		}
	}
	if !found {
		return s
	}

	origin := s
	var runeTmp [utf8.UTFMax]byte
	buf := make([]byte, 0, 3*len(s)/2)
	for len(s) > 0 {
		if s[0] == '\\' && len(s) > 1 && (s[1] == '"' || s[1] == '`') {
			buf = append(buf, s[1])
			s = s[2:]
		} else {
			c, multibyte, ss, err := strconv.UnquoteChar(s, '\'')
			if err != nil {
				return origin
			}
			s = ss
			if c < utf8.RuneSelf || !multibyte {
				buf = append(buf, byte(c))
			} else {
				n := utf8.EncodeRune(runeTmp[:], c)
				buf = append(buf, runeTmp[:n]...)
			}
		}
	}
	return string(buf)
}
