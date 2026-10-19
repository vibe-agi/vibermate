package runtimedata

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func TestPublishedRuntimeFormatsPublicBackupAndMove(t *testing.T) {
	for _, format := range []string{"v1", "v2", "daybreak_blue", "daybreak_red"} {
		t.Run(format, func(t *testing.T) {
			var source string
			revision := int64(3)
			switch format {
			case "v1":
				source = frozenUpgradeFixture(t)
				revision = 1
			case "v2":
				source = frozenRevision2UpgradeFixture(t)
				revision = 2
			default:
				source, _ = backupFixture(t)
				seedSnapshotAuthority(t, source)
				freezePublishedBackupFixture(t, source)
				db, err := sql.Open("sqlite", filepath.Join(source, "runtime.db"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(`UPDATE provider_accounts SET credential_origin='https://chatgpt.com',realm_id='openai.chatgpt',driver_ref='static_header',cyber_access_program=?,settings_revision=7,note='retained mode note',note_revision=3`, format); err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
			before := snapshotTestRows(t, source)
			root := filepath.Dir(source)
			backup := filepath.Join(root, "public-backup")
			restored := filepath.Join(root, "public-restore")
			moved := filepath.Join(root, "moved")
			if err := Backup(context.Background(), source, backup, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if err := Restore(context.Background(), backup, restored); err != nil {
				t.Fatal(err)
			}
			if err := Copy(context.Background(), source, moved); err != nil {
				t.Fatal(err)
			}
			for _, target := range []string{source, restored, moved} {
				readRoot := target
				if target == restored {
					layout, e := ReadStorageLayout(target)
					if e != nil {
						t.Fatal(e)
					}
					if len(layout.Archives) != 1 {
						t.Fatal("missing restored legacy archive")
					}
					readRoot = filepath.Join(target, layout.Archives[0].RelativeDirectory)
				}
				state, err := runtimepersistence.InspectSchema(context.Background(), filepath.Join(readRoot, "runtime.db"), time.Second)
				if err != nil || state.Revision != revision {
					t.Fatalf("faithful revision %+v %v", state, err)
				}
				if fmt.Sprint(before) != fmt.Sprint(snapshotTestRows(t, readRoot)) {
					t.Fatal("backup/move changed logical values")
				}
				if revision == 3 {
					store := openBackupControlReader(t, target)
					account, exists, err := store.ProviderAccountRepository().Load(context.Background(), "snapshot-account")
					if err != nil || !exists || account.CyberAccessProgram.WireValue() != format || account.SettingsRevision != 7 || account.NoteRevision != 3 {
						t.Fatalf("lost selected mode or revisions: %+v %v", account, err)
					}
					store.Shutdown(context.Background())
				}
			}
		})
	}
}
