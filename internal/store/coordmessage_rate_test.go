package store

import (
	"errors"
	"strconv"
	"testing"
)

func TestMessageRateIsLimitedPerAccount(t *testing.T) {
	st := openTest(t)
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ts := now()
	for i := 0; i < CoordMessageLimit; i++ {
		if _, err := tx.Exec(`INSERT INTO coord_messages(destination_kind,destination_id,sequence,sender_external_id,author_principal_id,author_kind,client_id,kind,intent,priority,body,created_at)
			VALUES('room','room-x',?,'claude:a','person:1','agent',?,'','','','x',?)`, i+1, "c"+strconv.Itoa(i), ts); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkMessageRateTx(tx, "person:1", "claude:a"); !errors.Is(err, ErrCoordRateLimited) {
		t.Fatalf("at the limit: %v", err)
	}
	if err := checkMessageRateTx(tx, "person:2", "claude:b"); err != nil {
		t.Fatalf("another account is not limited: %v", err)
	}
}
