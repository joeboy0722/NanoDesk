package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	bolt "go.etcd.io/bbolt"
)

type registry struct{ db *bolt.DB }

func openRegistry(path string) (*registry, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{"keys", "ids"} {
			if _, e := tx.CreateBucketIfNotExists([]byte(name)); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &registry{db}, nil
}
func (r *registry) identify(public []byte) (string, error) {
	if len(public) != 32 {
		return "", fmt.Errorf("invalid public key")
	}
	var id string
	err := r.db.Update(func(tx *bolt.Tx) error {
		keys, ids := tx.Bucket([]byte("keys")), tx.Bucket([]byte("ids"))
		key := []byte(hex.EncodeToString(public))
		if old := keys.Get(key); old != nil {
			id = string(old)
			return nil
		}
		for tries := 0; tries < 100; tries++ {
			n, e := rand.Int(rand.Reader, big.NewInt(9000000000))
			if e != nil {
				return e
			}
			id = fmt.Sprint(n.Int64() + 1000000000)
			if ids.Get([]byte(id)) != nil {
				continue
			}
			if e = ids.Put([]byte(id), key); e != nil {
				return e
			}
			return keys.Put(key, []byte(id))
		}
		return fmt.Errorf("ID allocation exhausted")
	})
	return id, err
}
