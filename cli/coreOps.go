/* A re-implementation of the amazing imapgrap in plain Golang.
Copyright (C) 2022  Torsten Sachse

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

package main

import "github.com/razziel89/go-imapgrab/core"

type coreOps interface {
	getAllFolders(cfg core.IMAPConfig, ops core.ImapgrabOps) ([]string, error)
	downloadFolder(cfg core.IMAPConfig, folders []string, maildirBase string, threads int) error
}

type corer struct{}

func (c *corer) getAllFolders(cfg core.IMAPConfig, ops core.ImapgrabOps) ([]string, error) {
	return core.GetAllFolders(cfg, ops)
}

func (c *corer) downloadFolder(
	cfg core.IMAPConfig, folders []string, maildirBase string, threads int,
) error {
	return core.DownloadFolder(cfg, folders, maildirBase, threads)
}
