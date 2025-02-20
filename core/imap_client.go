/* A re-implementation of the amazing imapgrap in plain Golang.
Copyright (C) 2022  Torsten Long

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package core

import (
	"fmt"
	"strings"
	"sync"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

const (
	folderListBuffer       = 10
	messageRetrievalBuffer = 20
)

// Make this a function pointer to simplify testing. Also takes a boolean to decide whether to use
// secure auth nor not (i.e. TLS). This errors out if insecure auth is chossen but anything other
// than "127.0.0.1" is passed as "addr".
var newImapClient = func(addr string, insecure bool) (imap imapOps, err error) {
	if !insecure {
		// Use automatic configuration of TLS options.
		imap, err = client.DialTLS(addr, nil)
	} else if !strings.HasPrefix(addr, "127.0.0.1:") {
		err = fmt.Errorf(
			"not allowing insecure auth for non-localhost address %s, use 127.0.0.1", addr,
		)
	} else {
		logWarning("using insecure connection to locahost")
		imap, err = client.Dial(addr)
	}
	return
}

type imapOps interface {
	Login(username string, password string) error
	List(ref string, name string, ch chan *imap.MailboxInfo) error
	Select(name string, readOnly bool) (*imap.MailboxStatus, error)
	Fetch(seqset *imap.SeqSet, items []imap.FetchItem, ch chan *imap.Message) error
	UidFetch(seqset *imap.SeqSet, items []imap.FetchItem, ch chan *imap.Message) error
	Logout() error
	Terminate() error
}

func authenticateClient(config IMAPConfig) (imapClient imapOps, err error) {
	if len(config.Password) == 0 {
		logError("empty password detected")
		err = fmt.Errorf("password not set")
		return nil, err
	}

	logInfo(fmt.Sprintf("connecting to server %s", config.Server))
	serverWithPort := fmt.Sprintf("%s:%d", config.Server, config.Port)
	if imapClient, err = newImapClient(serverWithPort, config.Insecure); err != nil {
		logError("cannot connect")
		return nil, err
	}
	logInfo("connected")

	logInfo(fmt.Sprintf("logging in as %s with provided password", config.User))
	if err = imapClient.Login(config.User, config.Password); err != nil {
		logError("cannot log in")
		return nil, err
	}
	logInfo("logged in")

	return imapClient, nil
}

func getFolderList(imapClient imapOps) (folders []string, err error) {
	logInfo("retrieving folders")
	mailboxes := make(chan *imap.MailboxInfo, folderListBuffer)
	go func() {
		err = imapClient.List("", "*", mailboxes)
	}()
	for m := range mailboxes {
		folders = append(folders, m.Name)
	}
	logInfo(fmt.Sprintf("retrieved %d folders", len(folders)))

	return folders, err
}

func selectFolder(imapClient imapOps, folder string) (*imap.MailboxStatus, error) {
	logInfo(fmt.Sprint("selecting folder:", folder))
	// Access the folder in read-only mode.
	mbox, err := imapClient.Select(folder, true)
	if err == nil {
		logInfo(fmt.Sprint("flags for selected folder are", mbox.Flags))
		logInfo(fmt.Sprintf("selected folder contains %d emails", mbox.Messages))
	}
	return mbox, err
}

// Type once behaves like sync.Once but we can also query whether it has already been called. This
// is needed because sync.Once does not provide a facility to check that.
type once struct {
	called bool
	hook   func()
	sync.Once
}

func (o *once) call() {
	o.Do(o.hook)
}

func newOnce(hook func()) *once {
	o := once{}
	innerHook := func() {
		o.called = true
		hook()
	}
	o.hook = innerHook
	return &o
}

// Obtain messages whose ids/indices lie in certain ranges. Negative indices are automatically
// converted to count from the last message. That is, -1 refers to the most recent message while 1
// refers to the second oldest email.
//
// In this function, we translate from *imap.Message to emailOps separately. Sadly, the compiler
// does not auto-generate the code to use a `chan emailOps` as a `chan *imap.Message`. Thus, we need
// a separate, second goroutine translating between the two. This second goroutine also handles
// interrupts.
func streamingRetrieval(
	imapClient imapOps,
	uids []uid,
	wg, startWg *sync.WaitGroup,
	interrupted func() bool,
) (returnedChan <-chan emailOps, errCountPtr *int, err error) {
	// Make sure all UIDs are >0.
	for _, uid := range uids {
		if uid <= 0 {
			return nil, nil, fmt.Errorf("detected a UID<=0, aborting")
		}
	}

	// Emails will be retrieved via a SeqSet, which can contain a set of messages.
	seqset := new(imap.SeqSet)
	for _, uid := range uids {
		seqset.AddNum(intToUint32(int(uid)))
	}

	wg.Add(1)
	// Ensure we call "Done" exactly once on wg here.
	already := newOnce(func() { wg.Done() })
	var errCount int
	translatedMessageChan := make(chan emailOps, messageRetrievalBuffer)
	orgMessageChan := make(chan *imap.Message)
	go func() {
		// Do not start before the entire pipeline has been set up.
		startWg.Wait()
		err := imapClient.UidFetch(
			seqset,
			[]imap.FetchItem{imap.FetchUid, imap.FetchInternalDate, imap.FetchRFC822},
			orgMessageChan,
		)
		if err != nil {
			logError(err.Error())
			errCount++
		}
		already.call()
	}()

	go func() {
		defer close(translatedMessageChan)
		for !already.called {
			if interrupted() {
				errCount++
				already.call()
				logWarning("caught keyboard interrupt, closing connection")
				// Clean up and report.
			} else {
				msg := <-orgMessageChan
				// Ignore nil values that we sometimes receive even though we should not.
				if msg != nil {
					// Here, the compiler generates code to convert `*imap.Message` into emailOps`.
					translatedMessageChan <- msg
				}
			}
		}
	}()

	return translatedMessageChan, &errCount, nil
}

// Type uid describes a message. It is a type alias to prevent accidental mixups.
type uid int

// Type uidFolder describes a mailbox. It is a type alias to prevent accidental mixups.
type uidFolder int

// Type uidExt describes a unique identifier for a message as well as the associated mailbox. It
// consists of the unique identifier of the mailbox the message belongs to and a unique identifier
// for a message within that mailbox.
type uidExt struct {
	folder uidFolder
	msg    uid
}

// String provides a string representation for a message's unique identifier.
func (u uidExt) String() string {
	return fmt.Sprintf("%d/%d", u.folder, u.msg)
}

func getAllMessageUUIDs(
	mbox *imap.MailboxStatus, imapClient imapOps,
) (uids []uidExt, err error) {
	logInfo("retrieving information about emails stored on server")
	// Handle the special case of empty folders by returning early.
	if mbox.Messages == 0 {
		return nil, nil
	}

	uids = make([]uidExt, 0, mbox.Messages)

	// Retrieve information about all emails.
	seqset := new(imap.SeqSet)
	seqset.AddRange(1, mbox.Messages)

	messageChannel := make(chan *imap.Message, messageRetrievalBuffer)
	go func() {
		err = imapClient.Fetch(
			seqset,
			[]imap.FetchItem{imap.FetchUid, imap.FetchInternalDate},
			messageChannel,
		)
	}()
	for m := range messageChannel {
		if m != nil {
			appUID := uidExt{
				folder: uidFolder(mbox.UidValidity),
				msg:    uid(m.Uid),
			}
			uids = append(uids, appUID)
		}
	}
	logInfo(fmt.Sprintf("received information for %d emails", len(uids)))

	return uids, err
}
