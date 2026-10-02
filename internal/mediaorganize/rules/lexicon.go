package rules

const (
	DefaultMediaExtensions    = "mkv;mp4;avi;ts;mov;wmv;iso;m2ts;rmvb;flv;m4v;webm"
	DefaultMetadataExtensions = "nfo;ass;ssa;srt;sub;idx;sup;vtt;jpg;jpeg;png;webp;bmp"
	MaxFilenameBytes          = 235
	MaxReasonableSeason       = 99
)

var MediaTagFields = []string{
	"screen_size", "frame_rate", "video_codec", "audio_codec", "audio_channels",
}

var DefaultMediaTagOrder = []string{
	"screen_size", "frame_rate", "video_codec", "audio_codec", "audio_channels",
}

// GenericMediaDirNames 是「通用媒体目录」白名单：这些目录只是落盘/中转位置，
// 不是用户表达的作品分类意图，整理时不应该被当成「分类层级」照搬或跳过建目录。
// 注意要覆盖常见的下载/临时落盘目录（下载、downloads、temp、未分类…），
// 漏掉它们会让 rename 模式下的散落文件只在原地改名，观感上等于「重命名完扔根目录」。
var GenericMediaDirNames = map[string]struct{}{
	"电影": {}, "影片": {}, "movie": {}, "movies": {},
	"电视剧": {}, "剧集": {}, "连续剧": {}, "tv": {}, "tv shows": {}, "shows": {}, "series": {},
	"动漫": {}, "动画": {}, "anime": {}, "media": {}, "video": {}, "videos": {}, "视频": {},
	// 下载/临时落盘目录
	"下载": {}, "download": {}, "downloads": {}, "temp": {}, "tmp": {},
	// 未整理/待归类目录
	"未分类": {}, "其他": {}, "未整理": {}, "待整理": {}, "unsorted": {}, "incoming": {},
}

var KnownReleaseGroups = map[string]struct{}{
	"CHD": {}, "CHDBits": {}, "CHDTV": {}, "CHDWEB": {}, "CHDPAD": {}, "CHDHKTV": {},
	"WiKi": {}, "MTeam": {}, "MTeamTV": {}, "ADE": {}, "ADWeb": {},
	"HDS": {}, "HDSky": {}, "HDH": {}, "HDC": {}, "HDArea": {}, "HDChina": {}, "HDCTV": {},
	"NTb": {}, "NTG": {}, "NTROPiC": {}, "FraMeSToR": {},
	"TLF": {}, "TLFCD": {}, "TLFGROUP": {},
	"OurBits": {}, "OurTV": {}, "OurPanda": {}, "iHD": {}, "OPS": {},
	"PuTao": {}, "Pter": {}, "PTHome": {},
	"DDR": {}, "TJUPT": {}, "JOY": {}, "BMDru": {},
	"FRDS": {}, "GREENOTEA": {}, "DBD": {}, "DKB": {}, "PandaMoon": {},
	"NF": {}, "AMZN": {}, "ATV": {}, "DSNP": {}, "HMAX": {}, "MAX": {}, "iT": {},
	"Sicario": {}, "Telly": {}, "TEPES": {}, "BTN": {}, "TRD": {}, "Pandamonium": {},
	"BiliBili": {}, "ByRA": {}, "ByMQ": {}, "NowYS": {}, "QHstudIo": {}, "RARBG": {},
	"YTS": {}, "YIFY": {}, "EVO": {}, "GalaxyRG": {}, "MeGusta": {},
	"FFans": {}, "MNHD": {}, "MTeamWEB": {},
	// 中文压制组 / 民间组
	"CMCT": {}, "beAst": {}, "BeAst": {}, "CHDWiKi": {}, "SUM": {}, "CEE": {},
	// 动漫字幕组（ASCII 尾部形态）
	"SweetSub": {}, "LoliHouse": {}, "Nekomoe": {}, "MCE": {}, "HYSUB": {}, "KTXP": {}, "MingY": {}, "ANi": {},
}

var knownReleaseGroupsCI map[string]struct{}

var resolutionLikeNumbers = map[int]struct{}{
	360: {}, 480: {}, 540: {}, 576: {}, 720: {}, 1080: {}, 1440: {}, 2160: {}, 4320: {},
}

func init() {
	knownReleaseGroupsCI = make(map[string]struct{}, len(KnownReleaseGroups))
	for g := range KnownReleaseGroups {
		knownReleaseGroupsCI[toLowerASCII(g)] = struct{}{}
	}
}

func isKnownReleaseGroup(s string) bool {
	_, ok := knownReleaseGroupsCI[toLowerASCII(s)]
	return ok
}
