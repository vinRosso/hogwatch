package cube

// defaultCategories mirrors the file_categories seed of migration 0001. The db
// package tests that the two agree.
var defaultCategories = map[string]Category{
	"mp4":   Video,
	"mkv":   Video,
	"avi":   Video,
	"mov":   Video,
	"wmv":   Video,
	"flv":   Video,
	"webm":  Video,
	"m4v":   Video,
	"mpg":   Video,
	"mpeg":  Video,
	"ts":    Video,
	"m2ts":  Video,
	"vob":   Video,
	"3gp":   Video,
	"mp3":   Audio,
	"flac":  Audio,
	"wav":   Audio,
	"aac":   Audio,
	"ogg":   Audio,
	"opus":  Audio,
	"m4a":   Audio,
	"wma":   Audio,
	"aiff":  Audio,
	"ape":   Audio,
	"jpg":   Image,
	"jpeg":  Image,
	"png":   Image,
	"gif":   Image,
	"bmp":   Image,
	"tif":   Image,
	"tiff":  Image,
	"webp":  Image,
	"heic":  Image,
	"heif":  Image,
	"raw":   Image,
	"cr2":   Image,
	"cr3":   Image,
	"nef":   Image,
	"arw":   Image,
	"dng":   Image,
	"svg":   Image,
	"psd":   Image,
	"zip":   Archive,
	"rar":   Archive,
	"7z":    Archive,
	"tar":   Archive,
	"gz":    Archive,
	"tgz":   Archive,
	"bz2":   Archive,
	"xz":    Archive,
	"zst":   Archive,
	"lz4":   Archive,
	"lzma":  Archive,
	"cab":   Archive,
	"iso":   DiskImage,
	"img":   DiskImage,
	"vmdk":  DiskImage,
	"vdi":   DiskImage,
	"qcow2": DiskImage,
	"vhd":   DiskImage,
	"vhdx":  DiskImage,
	"dmg":   DiskImage,
	"pdf":   Document,
	"doc":   Document,
	"docx":  Document,
	"xls":   Document,
	"xlsx":  Document,
	"ppt":   Document,
	"pptx":  Document,
	"odt":   Document,
	"ods":   Document,
	"odp":   Document,
	"rtf":   Document,
	"txt":   Document,
	"md":    Document,
	"epub":  Document,
	"csv":   Document,
	"go":    Code,
	"c":     Code,
	"h":     Code,
	"cpp":   Code,
	"hpp":   Code,
	"rs":    Code,
	"py":    Code,
	"js":    Code,
	"java":  Code,
	"kt":    Code,
	"rb":    Code,
	"php":   Code,
	"sh":    Code,
	"cs":    Code,
	"swift": Code,
	"sql":   Code,
	"json":  Code,
	"yaml":  Code,
	"yml":   Code,
	"toml":  Code,
	"html":  Code,
	"css":   Code,
}

// DefaultCategories returns a fresh copy of the seeded extension-to-category
// map; callers may modify it.
func DefaultCategories() map[string]Category {
	m := make(map[string]Category, len(defaultCategories))
	for k, v := range defaultCategories {
		m[k] = v
	}
	return m
}

// CategoryOf returns the category of name's extension in cats, or Other.
func CategoryOf(name []byte, cats map[string]Category) Category {
	if c, ok := cats[Ext(name)]; ok {
		return c
	}
	return Other
}
