package buntdb

import (
	"context"
	"sync"
	"time"

	"github.com/go-session/session"
	"github.com/json-iterator/go"
	"github.com/tidwall/buntdb"
)

var (
	_             session.ManagerStore = &managerStore{}
	_             session.Store        = &store{}
	jsonMarshal                        = jsoniter.Marshal
	jsonUnmarshal                      = jsoniter.Unmarshal
)

// NewMemoryStore Create an instance of a memory store
func NewMemoryStore() session.ManagerStore {
	db, err := buntdb.Open(":memory:")
	if err != nil {
		panic(err)
	}
	return newManagerStore(db)
}

// NewFileStore Create an instance of a file store
func NewFileStore(path string) session.ManagerStore {
	db, err := buntdb.Open(path)
	if err != nil {
		panic(err)
	}
	return newManagerStore(db)
}

func newManagerStore(db *buntdb.DB) *managerStore {
	return &managerStore{
		db: db,
		pool: sync.Pool{
			New: func() interface{} {
				return newStore(db)
			},
		},
	}
}

type managerStore struct {
	db   *buntdb.DB
	pool sync.Pool
}

func (s *managerStore) getValue(sid string) (string, error) {
	var value string

	err := s.db.View(func(tx *buntdb.Tx) error {
		val, err := tx.Get(sid)
		if err != nil {
			if err == buntdb.ErrNotFound {
				return nil
			}
			return err
		}
		value = val
		return nil
	})

	return value, err
}

func (s *managerStore) parseValue(value string) (map[string]interface{}, error) {
	var values map[string]interface{}

	if len(value) > 0 {
		err := jsonUnmarshal([]byte(value), &values)
		if err != nil {
			return nil, err
		}
	}

	return values, nil
}

func (s *managerStore) Check(_ context.Context, sid string) (bool, error) {
	val, err := s.getValue(sid)
	if err != nil {
		return false, err
	}
	return val != "", nil
}

func (s *managerStore) Create(ctx context.Context, sid string, expired int64) (session.Store, error) {
	store := s.pool.Get().(*store)
	store.reset(ctx, sid, expired, nil)
	return store, nil
}

func (s *managerStore) Update(ctx context.Context, sid string, expired int64) (session.Store, error) {
	store := s.pool.Get().(*store)

	value, err := s.getValue(sid)
	if err != nil {
		return nil, err
	} else if value == "" {
		store.reset(ctx, sid, expired, nil)
		return store, nil
	}

	err = s.db.Update(func(tx *buntdb.Tx) error {
		_, _, err := tx.Set(sid, value,
			&buntdb.SetOptions{Expires: true, TTL: time.Duration(expired) * time.Second})
		return err
	})
	if err != nil {
		return nil, err
	}

	values, err := s.parseValue(value)
	if err != nil {
		return nil, err
	}

	store.reset(ctx, sid, expired, values)
	return store, nil
}

func (s *managerStore) Delete(_ context.Context, sid string) error {
	return s.db.Update(func(tx *buntdb.Tx) error {
		_, err := tx.Delete(sid)
		if err == buntdb.ErrNotFound {
			return nil
		}
		return err
	})
}

func (s *managerStore) Refresh(ctx context.Context, oldsid, sid string, expired int64) (session.Store, error) {
	store := s.pool.Get().(*store)

	value, err := s.getValue(oldsid)
	if err != nil {
		return nil, err
	} else if value == "" {
		store.reset(ctx, sid, expired, nil)
		return store, nil
	}

	err = s.db.Update(func(tx *buntdb.Tx) error {
		_, _, err := tx.Set(sid, value,
			&buntdb.SetOptions{Expires: true, TTL: time.Duration(expired) * time.Second})
		if err != nil {
			return err
		}
		_, err = tx.Delete(oldsid)
		return err
	})
	if err != nil {
		return nil, err
	}

	values, err := s.parseValue(value)
	if err != nil {
		return nil, err
	}

	store.reset(ctx, sid, expired, values)
	return store, nil
}

func (s *managerStore) Close() error {
	return s.db.Close()
}

func newStore(db *buntdb.DB) *store {
	return &store{
		db: db,
	}
}

type store struct {
	sync.RWMutex
	ctx     context.Context
	sid     string
	expired int64
	db      *buntdb.DB
	values  map[string]interface{}
}

func (s *store) reset(ctx context.Context, sid string, expired int64, values map[string]interface{}) {
	if values == nil {
		values = make(map[string]interface{})
	}
	s.ctx = ctx
	s.sid = sid
	s.expired = expired
	s.values = values
}

func (s *store) Context() context.Context {
	return s.ctx
}

func (s *store) SessionID() string {
	return s.sid
}

func (s *store) Set(key string, value interface{}) {
	s.Lock()
	s.values[key] = value
	s.Unlock()
}

func (s *store) Get(key string) (interface{}, bool) {
	s.RLock()
	val, ok := s.values[key]
	s.RUnlock()
	return val, ok
}

func (s *store) Delete(key string) interface{} {
	s.RLock()
	v, ok := s.values[key]
	s.RUnlock()
	if ok {
		s.Lock()
		delete(s.values, key)
		s.Unlock()
	}
	return v
}

func (s *store) Flush() error {
	s.Lock()
	s.values = make(map[string]interface{})
	s.Unlock()
	return s.Save()
}

func (s *store) Save() error {
	var value string

	s.RLock()
	if len(s.values) > 0 {
		buf, err := jsonMarshal(s.values)
		if err != nil {
			s.RUnlock()
			return err
		}
		value = string(buf)
	}
	s.RUnlock()

	return s.db.Update(func(tx *buntdb.Tx) error {
		_, _, err := tx.Set(s.sid, value,
			&buntdb.SetOptions{Expires: true, TTL: time.Duration(s.expired) * time.Second})
		return err
	})
}
