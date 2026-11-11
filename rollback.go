package overseer

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Called with upgradeMux and the cooperating installer lock held. Keep the
// acknowledged image across multiple staged candidates while a worker is busy.
func (mp *master) saveRollback() error {
	if mp.rollbackPath != "" {
		return nil
	}
	mp.restartMux.Lock()
	active := append([]byte(nil), mp.forkedHash...)
	disk := append([]byte(nil), mp.binHash...)
	mp.restartMux.Unlock()
	if len(active) == 0 {
		// A startup update has no acknowledged worker yet. Preserve the image
		// the user launched so a failed first candidate can still be recovered.
		active = disk
	} else if mp.ReadyTimeout <= 0 {
		return nil
	}
	if !bytes.Equal(active, disk) {
		return fmt.Errorf("cannot preserve active executable: disk was replaced externally")
	}
	in, err := os.Open(mp.binPath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(mp.binPath), ".overseer-rollback-*"+extension())
	if err != nil {
		return err
	}
	path := out.Name()
	defer func() {
		out.Close()
		if mp.rollbackPath != path {
			os.Remove(path)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := chmod(out, mp.binPerms); err != nil {
		return err
	}
	if err := chown(out, uid, gid); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	hash, err := binaryHash(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(active, hash) {
		return fmt.Errorf("active executable changed while preserving rollback")
	}
	mp.rollbackPath, mp.rollbackHash = path, active
	return nil
}

func (mp *master) discardRollback() {
	if mp.rollbackPath != "" {
		_ = os.Remove(mp.rollbackPath)
	}
	mp.rollbackPath, mp.rollbackHash = "", nil
}

// Called before releasing the same installer lock used for activation.
func (mp *master) restoreRollback() error {
	if err := overwrite(mp.binPath, mp.rollbackPath); err != nil {
		return err
	}
	mp.restartMux.Lock()
	mp.binHash = mp.rollbackHash
	mp.startupCandidate = false
	mp.pendingRestart = false
	mp.restartMux.Unlock()
	mp.rollbackPath, mp.rollbackHash = "", nil
	return nil
}
