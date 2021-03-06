package canal

import (
	"github.com/juju/errors"
	"github.com/ngaut/log"
	"github.com/zjh1943/go-mysql/mysql"
)

var (
	ErrHandleInterrupted = errors.New("do handler error, interrupted")
)

type RowsEventHandler interface {
	// Handle RowsEvent, if return ErrHandleInterrupted, canal will
	// stop the sync
	Do(e *RowsEvent) error
	String() string
}



func (c *Canal) RegRowsEventHandler(h RowsEventHandler) {
	c.rsLock.Lock()
	c.rsHandlers = append(c.rsHandlers, h)
	c.rsLock.Unlock()
}

func (c *Canal) travelRowsEventHandler(e *RowsEvent) error {
	c.rsLock.Lock()
	defer c.rsLock.Unlock()

	var err error
	for _, h := range c.rsHandlers {
		if err = h.Do(e); err != nil && !mysql.ErrorEqual(err, ErrHandleInterrupted) {
			log.Errorf("handle %v err: %v", h, err)
		} else if mysql.ErrorEqual(err, ErrHandleInterrupted) {
			log.Errorf("handle %v err, interrupted", h)
			return ErrHandleInterrupted
		}

	}
	return nil
}

type ProgressHandler interface {
	OnDumpStart()
	OnDumpComplete()
}

func(c *Canal) RegProgressHandler( h ProgressHandler){
	c.psLock.Lock()
	c.psHandlers = append(c.psHandlers, h)
	c.psLock.Unlock()
}

func (c *Canal) onDumpStart(){
	for _,h := range c.psHandlers {
		h.OnDumpStart()
	}
}
func (c *Canal) onDumpComplete(){
	for _,h := range c.psHandlers {
		h.OnDumpComplete()
	}
}



