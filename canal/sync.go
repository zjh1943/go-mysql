package canal

import (
	"regexp"
	"time"

	"golang.org/x/net/context"

	"github.com/juju/errors"
	"github.com/ngaut/log"
	"github.com/zjh1943/go-mysql/mysql"
	"github.com/zjh1943/go-mysql/replication"
	"github.com/zjh1943/go-mysql/schema"
)

var (
	expAlterTable = regexp.MustCompile("(?i)^ALTER\\sTABLE\\s.*?`{0,1}(.*?)`{0,1}\\.{0,1}`{0,1}([^`\\.]+?)`{0,1}\\s.*")
)

func (c *Canal) startSyncBinlog() error {
	pos := mysql.Position{c.master.Name, c.master.Position}

	log.Infof("start sync binlog at %v", pos)

	s, err := c.syncer.StartSync(pos)
	if err != nil {
		return errors.Errorf("start sync replication at %v error %v", pos, err)
	}

	timeout := time.Second
	forceSavePos := false
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		ev, err := s.GetEvent(ctx)
		cancel()

		if err == context.DeadlineExceeded {
			timeout = 2 * timeout
			continue
		}

		if err != nil {
			return errors.Trace(err)
		}

		timeout = time.Second

		//next binlog pos
		pos.Pos = ev.Header.LogPos

		forceSavePos = false

		// We only save position with RotateEvent and XIDEvent.
		// For RowsEvent, we can't save the position until meeting XIDEvent
		// which tells the whole transaction is over.
		// TODO: If we meet any DDL query, we must save too.
		switch e := ev.Event.(type) {
		case *replication.RotateEvent:
			pos.Name = string(e.NextLogName)
			pos.Pos = uint32(e.Position)
			// r.ev <- pos
			forceSavePos = true
			log.Infof("rotate binlog to %v", pos)
		case *replication.RowsEvent:
			// we only focus row based event
			err = c.handleRowsEvent(ev)
			if err != nil && errors.Cause(err) != schema.ErrTableNotExist {
				// We can ignore table not exist error
				log.Errorf("handle rows event error %v", err)
				return errors.Trace(err)
			}
			continue
		case *replication.XIDEvent:
			// try to save the position later
		case *replication.QueryEvent:
			// handle alert table query
			if mb := expAlterTable.FindSubmatch(e.Query); mb != nil {
				if len(mb[1]) == 0 {
					mb[1] = e.Schema
				}
				c.ClearTableCache(mb[1], mb[2])
				log.Infof("table structure changed, clear table cache: %s.%s\n", mb[1], mb[2])
				forceSavePos = true
			} else {
				// skip others
				continue
			}
		default:
			continue
		}

		c.master.Update(pos.Name, pos.Pos)
		c.master.Save(forceSavePos)
	}

	return nil
}

func (c *Canal) handleRowsEvent(e *replication.BinlogEvent) error {
	ev := e.Event.(*replication.RowsEvent)

	// Caveat: table may be altered at runtime.
	schema := string(ev.Table.Schema)
	table := string(ev.Table.Table)

	// If db not in config file, ignore it
	needProcess := false
	if len(c.cfg.Dbs) <= 0 {
		needProcess = true
	} else {
		for _, v := range c.cfg.Dbs {
			if schema == v {
				needProcess = true
				break
			}
		}
	}
	if !needProcess {
		return nil
	}

	t, err := c.GetTable(schema, table)
	if err != nil {
		return errors.Trace(err)
	}
	var action string
	switch e.Header.EventType {
	case replication.WRITE_ROWS_EVENTv1, replication.WRITE_ROWS_EVENTv2:
		action = InsertAction
	case replication.DELETE_ROWS_EVENTv1, replication.DELETE_ROWS_EVENTv2:
		action = DeleteAction
	case replication.UPDATE_ROWS_EVENTv1, replication.UPDATE_ROWS_EVENTv2:
		action = UpdateAction
	default:
		return errors.Errorf("%s not supported now", e.Header.EventType)
	}
	events := newRowsEvent(t, action, ev.Rows)
	return c.travelRowsEventHandler(events)
}

func (c *Canal) WaitUntilPos(pos mysql.Position, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	for {
		select {
		case <-timer.C:
			return errors.Errorf("wait position %v too long > %s", pos, timeout)
		default:
			curPos := c.master.Pos()
			if curPos.Compare(pos) >= 0 {
				return nil
			} else {
				log.Debugf("master pos is %v, wait catching %v", curPos, pos)
				time.Sleep(100 * time.Millisecond)
			}
		}
	}

	return nil
}

func (c *Canal) CatchMasterPos(timeout time.Duration) error {
	rr, err := c.Execute("SHOW MASTER STATUS")
	if err != nil {
		return errors.Trace(err)
	}

	name, _ := rr.GetString(0, 0)
	pos, _ := rr.GetInt(0, 1)

	return c.WaitUntilPos(mysql.Position{name, uint32(pos)}, timeout)
}
