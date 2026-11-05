//
//  links.go
//  ropa
//
//  Created by p2k on 05.11.2026
//  Copyright (c) Patrick "p2k" Schneider
//
//  Licensed under the EUPL, Version 1.2 or – as soon they will be approved by
//  the European Commission - subsequent versions of the EUPL (the "Licence");
//  You may not use this work except in compliance with the Licence.
//  You may obtain a copy of the Licence at:
//
//  https://joinup.ec.europa.eu/collection/eupl/eupl-text-eupl-12
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the Licence is distributed on an "AS IS" basis,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the Licence for the specific language governing permissions and
//  limitations under the Licence.
//

package grf

import "slices"

// Link groups
//
// The GRF format has no notion of links: entries that share the same data
// offset are links of each other. Such entries form a link group. Its first
// member is the canonical file (empty Linkname), and every other member has
// the canonical file's name in its Linkname.
//
// Links behave like copy-on-write copies. Writing to a name gives only that
// entry new data, so it leaves its group, while the other members keep the old
// data. Deleting or renaming a member never changes the data of the others.
//
// The archive keeps an index of all groups with two or more members, keyed by
// offset, so these operations never have to scan the whole file table. Files
// without links are not part of the index.
//
// The canonical file is normally the first member in file table order, which
// is also how groups are derived when an archive is opened. Operations within
// one session can pick a different member; this only affects which name
// Linkname reports, never which data an entry refers to.

// rebuildLinks derives all link groups from the data offsets of the file
// table and sets every Linkname accordingly.
//
// Must be called with the write lock held (or before the archive is shared).
func (a *Archive) rebuildLinks() {
	canonical := make(map[uint32]*FileHeader, len(a.entriesInSequence))
	a.linkGroups = nil
	for _, h := range a.entriesInSequence {
		if h.Deleted {
			h.Linkname = ""
			continue
		}
		e := canonical[h.Offset]
		if e == nil {
			canonical[h.Offset] = h
			h.Linkname = ""
			continue
		}
		h.Linkname = e.Name
		if a.linkGroups == nil {
			a.linkGroups = make(map[uint32][]*FileHeader)
		}
		g := a.linkGroups[h.Offset]
		if g == nil {
			g = []*FileHeader{e}
		}
		a.linkGroups[h.Offset] = append(g, h)
	}
}

// leaveLinkGroup removes h from its link group, if it is in one, and clears
// its Linkname.
//
// If h was the canonical file, the next member takes over and the Linkname of
// the remaining members is updated. A group with only one member left is
// dissolved. Call this before changing h's offset.
func (a *Archive) leaveLinkGroup(h *FileHeader) {
	h.Linkname = ""
	g := a.linkGroups[h.Offset]
	i := slices.Index(g, h)
	if i < 0 {
		return
	}
	g = slices.Delete(g, i, i+1)
	if len(g) < 2 {
		delete(a.linkGroups, h.Offset)
		if len(g) == 1 {
			g[0].Linkname = ""
		}
		return
	}
	a.linkGroups[h.Offset] = g
	if i == 0 {
		g[0].Linkname = ""
		for _, e := range g[1:] {
			e.Linkname = g[0].Name
		}
	}
}

// joinLinkGroup adds link to the link group of target, creating the group if
// target has no links yet, and sets link's Linkname.
//
// link must not be in a group (see leaveLinkGroup), target must not be
// deleted, and the caller is responsible for copying target's offset.
func (a *Archive) joinLinkGroup(link, target *FileHeader) {
	if a.linkGroups == nil {
		a.linkGroups = make(map[uint32][]*FileHeader)
	}
	g := a.linkGroups[target.Offset]
	if g == nil {
		g = []*FileHeader{target}
	}
	a.linkGroups[target.Offset] = append(g, link)
	link.Linkname = g[0].Name
}

// renameInLinkGroup updates the Linkname of the other members after h has
// been renamed, if h is the canonical file of a group.
func (a *Archive) renameInLinkGroup(h *FileHeader) {
	g := a.linkGroups[h.Offset]
	if len(g) == 0 || g[0] != h {
		return
	}
	for _, e := range g[1:] {
		e.Linkname = h.Name
	}
}

// syncLinkGroup copies the properties of the shared data from h to the other
// members of its link group.
//
// Use this after changing the data in place.
func (a *Archive) syncLinkGroup(h *FileHeader) {
	for _, e := range a.linkGroups[h.Offset] {
		if e == h {
			continue
		}
		e.GRFEditorEncrypted = h.GRFEditorEncrypted
		e.GRFEditorEncryptionChecked = h.GRFEditorEncryptionChecked
		e.StreamHash = h.StreamHash
		e.Hash = h.Hash
	}
}
