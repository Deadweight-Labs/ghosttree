package store

import "context"

func OpenRuntime(path string, cfg WriterConfig) (*Store, error) {
	if sqliteFilePath(path) == "" || cfg.MaxOperations <= 0 || cfg.MaxBytes <= 0 || cfg.MaxBatch <= 0 || cfg.ReadConnections <= 0 {
		return nil, ErrWriterInvalidConfig
	}
	s, err := OpenWithOptions(path, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		return nil, err
	}
	if err := ProbeContextSnapshotSchema(context.Background(), s.db); err != nil {
		_ = s.Close()
		return nil, err
	}
	s.reader, err = OpenReadOnly(path, cfg.ReadConnections)
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	s.writer, err = newRuntimeWriter(cfg)
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	s.writer.chunkWrite = func(batches []ChunkBatch) error { return s.direct().AppendChunkBatches(batches) }
	return s, nil
}

func (s *Store) direct() *Store {
	return &Store{db: s.db, path: s.path, snapshotFault: s.snapshotFault}
}

func queueValue[T any](s *Store, payload []any, fn func(*Store, []any) (T, error)) (T, error) {
	var result T
	r, err := s.writer.admitOwned(context.Background(), payload, func(owned []any) error {
		var inner error
		result, inner = fn(s.direct(), owned)
		return inner
	})
	if err != nil {
		return result, err
	}
	err = <-r.done
	return result, err
}

func queueWrite(s *Store, payload []any, fn func(*Store, []any) error) error {
	_, err := queueValue(s, payload, func(d *Store, owned []any) (struct{}, error) { return struct{}{}, fn(d, owned) })
	return err
}
