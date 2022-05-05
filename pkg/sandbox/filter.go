package sandbox

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/blacktop/ipsw/pkg/dyld"
)

const (
	ARG_BOOLEAN              = 1 // boolean
	ARG_OCTAL                = 2 // bit pattern
	ARG_OWNER                = 3 // integer
	ARG_CTRL                 = 4 // string
	ARG_STRING_OFFSET        = 5 // pattern
	ARG_RSS_OFFSET           = 6 // pattern
	ARG_NOT_SURE_YET         = 7 // pattern TODO: fix this
	ARG_RSS_OFFSET_WITH_TYPE = 8 // path
	ARG_NETWORK_OFFSET       = 9 // pattern
	// ??           = 9 // regex
	// ??           = 10 // network
)

//go:embed data/libsandbox_12.3.0.gz
var libsandboxData []byte

type LibSandbox struct {
	Operations []OperationInfo `json:"operations,omitempty"`
	Filters    []FilterInfo    `json:"filters,omitempty"`
	Modifiers  []ModifierInfo  `json:"modifiers,omitempty"`
}

func GetLibSandBoxDB() (*LibSandbox, error) {
	var lb *LibSandbox

	zr, err := gzip.NewReader(bytes.NewReader(libsandboxData))
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	if err := json.NewDecoder(zr).Decode(&lb); err != nil {
		return nil, fmt.Errorf("failed to decode libsandbox data: %w", err)
	}

	return lb, nil
}

func (db *LibSandbox) GetFilter(id uint8) (*FilterInfo, error) {
	if int(id) >= len(db.Filters) {
		return nil, fmt.Errorf("invalid filter id: %d", id)
	}
	return &db.Filters[id], nil
}

func (f *FilterInfo) GetArgument(sb *Sandbox, id uint16) (any, error) {
	switch f.DataType {
	case ARG_BOOLEAN:
		if id == 1 {
			return "#t", nil
		}
		return "#f", nil
	case ARG_OCTAL:
		return fmt.Sprintf("#o%04o", id), nil
	case ARG_CTRL: // Convert integer value to IO control string
		alias, err := f.Aliases.Get(id) // TODO: why do these have aliases?
		if err != nil {
			return fmt.Sprintf("_IO \"%c\" %d", rune(id>>8), int(id&0xff)), nil
		}
		return alias.Name, nil
	case ARG_OWNER:
		alias, err := f.Aliases.Get(id)
		if err != nil {
			return nil, err
		}
		return alias.Name, nil
	case ARG_STRING_OFFSET:
		s, err := sb.GetStringAtOffset(uint32(id))
		if err != nil {
			return nil, err
		}
		return fmt.Sprintf("\"%s\"", s), nil
	case ARG_RSS_OFFSET, ARG_NOT_SURE_YET:
		ss, err := sb.GetRSStringAtOffset(uint32(id))
		if err != nil {
			return nil, err
		}
		return strings.Join(ss, " "), nil
	case ARG_RSS_OFFSET_WITH_TYPE:
		ss, err := sb.GetRSStringAtOffset(uint32(id))
		if err != nil {
			return nil, err
		}
		return []string{"literal", strings.Join(ss, " ")}, nil
	case ARG_NETWORK_OFFSET:
		host, port, err := sb.GetHostPortAtOffset(uint32(id))
		if err != nil {
			return nil, err
		}
		hostStr := "*"
		if host > 0x100 {
			hostStr = "localhost"
			host &= 0xff
		}
		alias, err := f.Aliases.Get(host)
		if err != nil {
			return nil, err
		}
		portStr := "*"
		if port != 0 {
			portStr = fmt.Sprintf("%d", port)
		}
		return fmt.Sprintf("%s \"%s:%s\"", alias.Name, hostStr, portStr), nil
	default:
		return nil, fmt.Errorf("unsupported filter category: %s", f.Category)
	}
}

/**********************
 * LibSandbox Builder *
 **********************/

type FilterInfo struct {
	ID       int     `json:"id"`
	Name     string  `json:"name,omitempty"`
	Category string  `json:"category,omitempty"`
	Aliases  Aliases `json:"aliases,omitempty"`
	filterInfo
}

type filterInfo struct {
	NameAddr     uint64 `json:"-"`
	CategoryAddr uint64 `json:"-"`
	DataType     uint8  `json:"data_type,omitempty"`
	Namespace    uint8  `json:"namespace,omitempty"`
	SideEffects  uint16 `json:"side_effects,omitempty"`
	Dependency   uint32 `json:"dependency,omitempty"`
	AliasesAddr  uint64 `json:"-"`
}

type Aliases []Alias

func (a Aliases) Get(id uint16) (*Alias, error) {
	for _, alias := range a {
		if alias.ID == id {
			return &alias, nil
		}
	}
	return nil, fmt.Errorf("alias id not found: %d", id)
}

type Alias struct {
	Name string `json:"name,omitempty"`
	alias
}

type alias struct {
	NameAddr uint64 `json:"-"`
	ID       uint16 `json:"id,omitempty"`
	Unknown  uint16 `json:"unknown,omitempty"`
	Padding  uint32 `json:"-"`
}

func GetFilterInfo(d *dyld.File) ([]FilterInfo, error) {
	var finfos []FilterInfo

	libsand, err := d.Image("libsandbox.1.dylib")
	if err != nil {
		return nil, fmt.Errorf("failed to get libsandbox.1.dylib image: %w", err)
	}

	m, err := libsand.GetMacho()
	if err != nil {
		return nil, fmt.Errorf("failed to get libsandbox.1.dylib macho: %w", err)
	}

	filterInfoAddr, err := m.FindSymbolAddress("_filter_info")
	if err != nil {
		return nil, fmt.Errorf("failed to find _filter_info symbol: %w", err)
	}
	uuid, filterInfoOff, err := d.GetOffset(filterInfoAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to get _filter_info offset: %w", err)
	}

	// TODO: maybe get filter info count from xref to _lookup_filter_info (is 0x55 in test libsandbox)
	NUM_INFO_ENTRIES := 0x55
	dat, err := d.ReadBytesForUUID(uuid, int64(filterInfoOff), uint64((NUM_INFO_ENTRIES+1)*binary.Size(filterInfo{})))
	if err != nil {
		return nil, fmt.Errorf("failed to read _filter_info data: %w", err)
	}

	r := bytes.NewReader(dat)

	for i := 0; i <= NUM_INFO_ENTRIES; i++ {
		fi := FilterInfo{ID: i}
		if err := binary.Read(r, binary.LittleEndian, &fi.filterInfo); err != nil {
			return nil, fmt.Errorf("failed to read _filter_info item %d: %w", i, err)
		}
		if fi.NameAddr != 0 {
			fi.Name, err = d.GetCString(d.SlideInfo.SlidePointer(fi.NameAddr))
			if err != nil {
				return nil, fmt.Errorf("failed to read _filter_info item %d name: %w", i, err)
			}
		}
		if fi.CategoryAddr != 0 {
			fi.Category, err = d.GetCString(d.SlideInfo.SlidePointer(fi.CategoryAddr))
			if err != nil {
				return nil, fmt.Errorf("failed to read _filter_info item %d category: %w", i, err)
			}
		}
		if fi.AliasesAddr != 0 {
			// parse aliases
			next := uint64(0)
			sizeOfAlias := uint64(binary.Size(alias{}))
			for {
				var a Alias
				uuid, off, err := d.GetOffset(d.SlideInfo.SlidePointer(fi.AliasesAddr) + next)
				if err != nil {
					return nil, fmt.Errorf("failed to get alias offset for addr %#x: %w", d.SlideInfo.SlidePointer(fi.AliasesAddr)+next, err)
				}
				dat, err := d.ReadBytesForUUID(uuid, int64(off), sizeOfAlias)
				if err != nil {
					return nil, fmt.Errorf("failed to read alias data: %w", err)
				}
				if err := binary.Read(bytes.NewReader(dat), binary.LittleEndian, &a.alias); err != nil {
					return nil, fmt.Errorf("failed to read alias: %w", err)
				}
				if a.NameAddr == 0 {
					break
				}
				a.Name, err = d.GetCString(d.SlideInfo.SlidePointer(a.NameAddr))
				if err != nil {
					return nil, fmt.Errorf("failed to read alias name: %w", err)
				}
				fi.Aliases = append(fi.Aliases, a)
				next += uint64(sizeOfAlias)
			}
		}
		finfos = append(finfos, fi)
	}

	return finfos, nil
}

type ModifierInfo struct {
	ID      int     `json:"id"`
	Name    string  `json:"name,omitempty"`
	Aliases []Alias `json:"aliases,omitempty"`
	modifierInfo
}

type modifierInfo struct {
	NameAddr    uint64 `json:"-"`
	ActionMask  uint32 `json:"action_mask,omitempty"`
	ActionFlag  uint32 `json:"action_flag,omitempty"`
	Unknown1    uint32 `json:"unknown_1,omitempty"`
	Unknown2    uint32 `json:"unknown_2,omitempty"`
	AliasesAddr uint64 `json:"-"`
}

func GetModifierInfo(d *dyld.File) ([]ModifierInfo, error) {
	var minfos []ModifierInfo

	libsand, err := d.Image("libsandbox.1.dylib")
	if err != nil {
		return nil, fmt.Errorf("failed to find libsandbox.1.dylib: %w", err)
	}

	m, err := libsand.GetMacho()
	if err != nil {
		return nil, fmt.Errorf("failed to get libsandbox.1.dylib macho: %w", err)
	}

	modifierInfoAddr, err := m.FindSymbolAddress("_modifier_info")
	if err != nil {
		return nil, fmt.Errorf("failed to find _modifier_info symbol: %w", err)
	}
	uuid, modifierInfoOff, err := d.GetOffset(modifierInfoAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to get _modifier_info offset: %w", err)
	}

	// TODO: maybe get SB_MODIFIER_COUNT from xref to ___sb_modifiers_apply_action_flags_block_invoke (is 0x15 in test libsandbox)
	SB_MODIFIER_COUNT := 0x15
	dat, err := d.ReadBytesForUUID(uuid, int64(modifierInfoOff), uint64(SB_MODIFIER_COUNT*binary.Size(modifierInfo{})))
	if err != nil {
		return nil, fmt.Errorf("failed to read _modifier_info data: %w", err)
	}

	r := bytes.NewReader(dat)

	for i := 0; i < SB_MODIFIER_COUNT; i++ {
		mi := ModifierInfo{ID: i}
		if err := binary.Read(r, binary.LittleEndian, &mi.modifierInfo); err != nil {
			return nil, fmt.Errorf("failed to read _modifier_info item %d: %w", i, err)
		}
		if mi.NameAddr != 0 {
			mi.Name, err = d.GetCString(d.SlideInfo.SlidePointer(mi.NameAddr))
			if err != nil {
				return nil, fmt.Errorf("failed to read _modifier_info item %d name: %w", i, err)
			}
		}
		if mi.AliasesAddr != 0 {
			// parse aliases
			next := uint64(0)
			sizeOfAlias := uint64(binary.Size(alias{}))
			for {
				var a Alias
				uuid, off, err := d.GetOffset(d.SlideInfo.SlidePointer(mi.AliasesAddr) + next)
				if err != nil {
					return nil, fmt.Errorf("failed to get alias offset for addr %#x: %w", d.SlideInfo.SlidePointer(mi.AliasesAddr)+next, err)
				}
				dat, err := d.ReadBytesForUUID(uuid, int64(off), sizeOfAlias)
				if err != nil {
					return nil, fmt.Errorf("failed to read alias data: %w", err)
				}
				if err := binary.Read(bytes.NewReader(dat), binary.LittleEndian, &a.alias); err != nil {
					return nil, fmt.Errorf("failed to read alias: %w", err)
				}
				if a.NameAddr == 0 {
					break
				}
				a.Name, err = d.GetCString(d.SlideInfo.SlidePointer(a.NameAddr))
				if err != nil {
					return nil, fmt.Errorf("failed to read alias name: %w", err)
				}
				mi.Aliases = append(mi.Aliases, a)
				next += uint64(sizeOfAlias)
			}
		}
		minfos = append(minfos, mi)
	}

	return minfos, nil
}

type OperationInfo struct {
	ID         int      `json:"id,omitempty"`
	Name       string   `json:"name,omitempty"`
	Modifiers  []string `json:"modifiers,omitempty"`
	Categories []string `json:"categories,omitempty"`
	Unknown1   uint32   `json:"unknown1,omitempty"`
	operationInfo
}

type operationInfo struct {
	Version             uint32 `json:"version,omitempty"`
	JumpTargetOperation uint32 `json:"jump_target_operation,omitempty"`
	Unknown2            uint64 `json:"unknown2,omitempty"`
	CategoriesAddr      uint64 `json:"-"`
	ModifiersAddr       uint64 `json:"-"`
	UnknownAddr         uint64 `json:"-"`
}

func GetOperationInfo(d *dyld.File) ([]OperationInfo, error) {
	var opNames []string
	var opInfos []OperationInfo

	libsand, err := d.Image("libsandbox.1.dylib")
	if err != nil {
		return nil, fmt.Errorf("failed to get image libsandbox.1.dylib: %w", err)
	}

	m, err := libsand.GetMacho()
	if err != nil {
		return nil, fmt.Errorf("failed to get macho for libsandbox.1.dylib: %w", err)
	}

	operationNamesAddr, err := m.FindSymbolAddress("_operation_names")
	if err != nil {
		return nil, fmt.Errorf("failed to find _operation_names symbol: %w", err)
	}
	uuid, operationNamesOff, err := d.GetOffset(operationNamesAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to get _operation_names offset: %w", err)
	}

	operationInfoAddr, err := m.FindSymbolAddress("_operation_info")
	if err != nil {
		return nil, fmt.Errorf("failed to find _operation_info symbol: %w", err)
	}

	dat, err := d.ReadBytesForUUID(uuid, int64(operationNamesOff), operationInfoAddr-operationNamesAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to read _operation_names data: %w", err)
	}

	onAddrs := make([]uint64, len(dat)/binary.Size(uint64(0)))
	if err := binary.Read(bytes.NewReader(dat), binary.LittleEndian, &onAddrs); err != nil {
		return nil, fmt.Errorf("failed to read _operation_names addrs: %w", err)
	}

	for _, addr := range onAddrs {
		name, err := d.GetCString(d.SlideInfo.SlidePointer(addr))
		if err != nil {
			return nil, fmt.Errorf("failed to read operation name: %w", err)
		}
		opNames = append(opNames, name)
	}

	uuid, operationInfoOff, err := d.GetOffset(operationInfoAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to get _operation_info offset: %w", err)
	}

	dat, err = d.ReadBytesForUUID(uuid, int64(operationInfoOff), uint64(len(opNames)*binary.Size(operationInfo{})))
	if err != nil {
		return nil, fmt.Errorf("failed to read _operation_info data: %w", err)
	}

	oinfos := make([]operationInfo, len(opNames))
	if err := binary.Read(bytes.NewReader(dat), binary.LittleEndian, &oinfos); err != nil {
		return nil, fmt.Errorf("failed to read _operation_info(s): %w", err)
	}

	for idx, oi := range oinfos {
		oinfo := OperationInfo{
			ID:            idx,
			Name:          opNames[idx],
			operationInfo: oi,
		}
		if oi.CategoriesAddr != 0 {
			// parse catergories
			next := uint64(0)
			for {
				addr, err := d.ReadPointerAtAddress(d.SlideInfo.SlidePointer(oi.CategoriesAddr) + next)
				if err != nil {
					return nil, fmt.Errorf("failed to read category addr at %#x: %w", d.SlideInfo.SlidePointer(oi.CategoriesAddr)+next, err)
				}
				if addr == 0 {
					break
				}
				cat, err := d.GetCString(d.SlideInfo.SlidePointer(addr))
				if err != nil {
					return nil, fmt.Errorf("failed to read category at %#x: %w", addr, err)
				}
				oinfo.Categories = append(oinfo.Categories, cat)
				next += uint64(binary.Size(uint64(0)))
			}
		}
		if oi.ModifiersAddr != 0 {
			// parse modifiers
			next := uint64(0)
			for {
				addr, err := d.ReadPointerAtAddress(d.SlideInfo.SlidePointer(oi.ModifiersAddr) + next)
				if err != nil {
					return nil, fmt.Errorf("failed to read modifier addr at %#x: %w", d.SlideInfo.SlidePointer(oi.ModifiersAddr)+next, err)
				}
				if addr == 0 {
					break
				}
				mod, err := d.GetCString(d.SlideInfo.SlidePointer(addr))
				if err != nil {
					return nil, fmt.Errorf("failed to read modifier at %#x: %w", addr, err)
				}
				oinfo.Modifiers = append(oinfo.Modifiers, mod)
				next += uint64(binary.Size(uint64(0)))
			}
		}
		if oi.UnknownAddr != 0 {
			uuid, off, err := d.GetOffset(d.SlideInfo.SlidePointer(oi.UnknownAddr))
			if err != nil {
				return nil, fmt.Errorf("failed to get _operation_info unknown struct offset: %w", err)
			}
			dat, err = d.ReadBytesForUUID(uuid, int64(off), uint64(len(opNames)*binary.Size(uint32(0))))
			if err != nil {
				return nil, fmt.Errorf("failed to read _operation_info unknown struct data: %w", err)
			}
			if err := binary.Read(bytes.NewReader(dat), binary.LittleEndian, &oinfo.Unknown1); err != nil {
				return nil, fmt.Errorf("failed to read _operation_info unknown uint32 data: %w", err)
			}
			// if oinfo.Unknown1 != 0 {
			// 	log.Log.Debugf("%s: %d", oinfo.Name, oinfo.Unknown1)
			// }
		}
		opInfos = append(opInfos, oinfo)
	}

	return opInfos, nil
}
