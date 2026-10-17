// Package lianlianpay 实现连连收单 OpenAPI 的纯协议部分：签名、验签、时间戳、
// 网关地址与报文构造。这里不涉及数据库、日志和 HTTP 传输，便于用官方向量做
// 单元测试。
//
// 协议要点（对齐 https://api-doc.lianlianpay.com/openplatform/doc-338855）：
//   - 签名 = Base64(RSA-MD5(MD5(完整 JSON 明文).hex))，放在 Signature-Data 请求头，
//     不是报文字段；因此必须对「实际发送的报文」加签。
//   - 时间戳 / txn_time 为东八区 yyyyMMddHHmmss。
//   - 异步通知同样用 Signature-Data 头承载签名，用连连平台公钥对原文验签。
package lianlianpay

import (
	"crypto"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	EnvTest = "test"
	EnvProd = "prod"

	// GatewayTest / GatewayProd 是官方文档网关，管理员可通过配置覆盖。
	GatewayTest = "https://openapi-test2.lianlianpay-inc.com/mch"
	GatewayProd = "https://openapi.lianlianpay.com/mch"

	// QueryGatewayTest / QueryGatewayProd 是收款查询接口的网关基址。
	QueryGatewayTest = "https://mpay-openapi-ste2.lianlianpay.com/query-api"
	QueryGatewayProd = "https://openapi.lianlianpay.com/query"

	// CreatePayPath 是收单统一创单接口路径。
	CreatePayPath = "/v1/ipay/createpay"

	// OrderQueryPath 是收款结果查询接口路径。
	OrderQueryPath = "/v1/ipay/orderquery"

	// PayExpireMinutes 是扫码订单的有效期（分钟），与钱包页倒计时保持一致。
	PayExpireMinutes = 15

	// SignTypeRSA 是连连支持的签名类型。
	SignTypeRSA = "RSA"

	// TimestampLayout 是连连要求的时间戳格式（东八区）。
	TimestampLayout = "20060102150405"

	// DefaultBusiType 是默认业务类型（普通收款）。
	DefaultBusiType = "100001"

	// RetCodeSuccess 表示连连受理成功。
	RetCodeSuccess = "0000"
	// RetCodeOrderExpired 表示订单已过期或关闭。
	RetCodeOrderExpired = "2003"
	// RetCodeOrderNotFound 表示查询不到原订单。
	RetCodeOrderNotFound = "4001"

	// DefaultDeviceID 是 PC 扫码场景的设备标识。
	DefaultDeviceID = "new-api"

	// TimeoutSeconds 是调用连连下单接口的超时时间（秒）。
	TimeoutSeconds = 15

	// SuccessResponse 是回调成功时返回给连连的纯文本。
	SuccessResponse = "Success"
	// FailResponse 是回调失败时返回给连连的纯文本。
	FailResponse = "fail"
)

// 支付方式：钱包侧 type 与连连 pay_type 的映射。仅支持 PC 扫码（native），
// 不接收聚合码。
const (
	PayMethodAlipay     = "alipay"
	PayMethodWxpay      = "wxpay"
	PayTypeAlipayNative = "ALIPAY_NATIVE"
	PayTypeWechatNative = "WECHAT_NATIVE"

	// HeaderMchID / HeaderSignatureType / HeaderSignatureData / HeaderTimestamp
	// 是连连创单接口要求的请求头。
	HeaderMchID         = "mch_id"
	HeaderSignatureType = "Signature-Type"
	HeaderSignatureData = "Signature-Data"
	HeaderTimestamp     = "timestamp"
	// HeaderContentTypeValue 是连连要求的报文类型。
	HeaderContentTypeValue = "application/json;charset=utf-8"
)

// PayTypeMap 把钱包支付方式映射为连连 pay_type。
var PayTypeMap = map[string]string{
	PayMethodAlipay: PayTypeAlipayNative,
	PayMethodWxpay:  PayTypeWechatNative,
}

// PayTypeOf 返回支付方式对应的连连 pay_type，不支持时返回空串。
func PayTypeOf(payMethod string) string {
	return PayTypeMap[strings.TrimSpace(payMethod)]
}

// PayMethodForPayType 把连连返回的 pay_type 映射回钱包侧支付方式。
func PayMethodForPayType(payType string) string {
	switch strings.ToUpper(strings.TrimSpace(payType)) {
	case PayTypeAlipayNative:
		return PayMethodAlipay
	case PayTypeWechatNative:
		return PayMethodWxpay
	default:
		return ""
	}
}

// SupportsPayMethod 报告支付方式是否有对应的连连 pay_type。
func SupportsPayMethod(payMethod string) bool {
	return PayTypeOf(payMethod) != ""
}

// PlatformPublicKey 是连连文档公布的平台公钥，仅在管理员未配置平台公钥时使用。
const PlatformPublicKey = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQCSS/DiwdCf/aZsxxcacDnooGph3d2JOj5GXWi+q3gznZauZjkNP8SKl3J2liP0O6rU/Y/29+IUe+GTMhMOFJuZm1htAtKiu5ekW0GlBMWxf4FPkYlQkPE0FtaoMP3gYfh+OwI+fIRrpW3ySn3mScnc6Z700nU/VYrRkfcSCbSnRwIDAQAB
-----END PUBLIC KEY-----
`

// ResolvePlatformKey 返回生效的平台公钥：管理员配置优先，缺省用内置公钥。
func ResolvePlatformKey(configured string) string {
	if strings.TrimSpace(configured) != "" {
		return configured
	}
	return PlatformPublicKey
}

var (
	// ErrEmptyPrivateKey 在商户私钥缺失时返回，调用方据此拒绝下单。
	ErrEmptyPrivateKey = errors.New("lianlian 商户私钥未配置")
	// ErrEmptyPublicKey 在平台公钥缺失且内置公钥被清空时返回。
	ErrEmptyPublicKey = errors.New("lianlian 平台公钥未配置")
	// ErrGatewayMissingMchSuffix 在管理员配置的网关地址不以 /mch 结尾时返回。
	// 连连收单接口固定在 {网关基址}/v1/ipay/createpay，基址必须含 /mch。
	ErrGatewayMissingMchSuffix = errors.New("连连网关地址必须以 /mch 结尾，例如 https://openapi.lianlianpay.com/mch")
)

// NowTimestamp 返回连连要求的 yyyyMMddHHmmss 时间戳，固定使用东八区，避免
// 部署在其它时区时签名数据与连连服务器不一致（否则报 ret_code=1007）。
func NowTimestamp() string {
	return FormatTimestamp(time.Now())
}

// FormatTimestamp 按连连要求把任意时刻格式化为东八区 yyyyMMddHHmmss。
func FormatTimestamp(moment time.Time) string {
	return moment.In(ShanghaiLocation()).Format(TimestampLayout)
}

// ShanghaiLocation 返回东八区时区，缺少 tzdata 时退化为固定偏移。
func ShanghaiLocation() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*60*60)
	}
	return location
}

// GatewayURL 返回收单创单接口的完整地址。override 非空时原样作为网关基址使用，
// 只允许追加创单路径；基址必须以 /mch 结尾，否则返回 ErrGatewayMissingMchSuffix。
// 未配置时使用官方测试/生产网关。
func GatewayURL(env string, override string) (string, error) {
	base := strings.TrimSpace(override)
	if base == "" {
		base = GatewayTest
		if env == EnvProd {
			base = GatewayProd
		}
	}
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, "/mch") {
		return "", ErrGatewayMissingMchSuffix
	}
	return base + CreatePayPath, nil
}

// QueryGatewayURL 返回收款查询接口的完整地址。未配置自定义网关时使用
// 官方测试/生产查询网关；配置了创单网关时，使用同一主机的 /query 基址。
func QueryGatewayURL(env string, override string) (string, error) {
	base := strings.TrimSpace(override)
	if base == "" {
		base = QueryGatewayTest
		if env == EnvProd {
			base = QueryGatewayProd
		}
	} else {
		base = strings.TrimRight(base, "/")
		if strings.HasSuffix(base, "/mch") {
			base = strings.TrimSuffix(base, "/mch")
			switch base {
			case strings.TrimSuffix(GatewayTest, "/mch"):
				base = QueryGatewayTest
			case strings.TrimSuffix(GatewayProd, "/mch"):
				base = QueryGatewayProd
			default:
				base += "/query"
			}
		}
	}
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, "/query") && !strings.HasSuffix(base, "/query-api") {
		return "", errors.New("连连查询网关地址必须以 /query 或 /query-api 结尾")
	}
	return base + OrderQueryPath, nil
}

// md5HexDigest 返回 MD5(MD5(body).hex)：连连先对报文取 MD5 得到十六进制串，
// 再对该字符串做 MD5withRSA，因此签名算法的输入摘要来自两层 MD5。
func md5HexDigest(body []byte) []byte {
	sum := md5.Sum([]byte(Md5Hex(body)))
	return sum[:]
}

// Md5Hex 返回入参的 32 位小写 MD5 十六进制串，是连连签名链的第一步。
func Md5Hex(body []byte) string {
	sum := md5.Sum(body)
	return hex.EncodeToString(sum[:])
}

// Sign 计算连连签名：Base64(RSA-MD5(MD5(body).hex))。body 必须是实际发送的
// 完整 JSON 明文；改一个空格都会导致验签失败。
func Sign(privateKeyPEM string, body []byte) (string, error) {
	key, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return "", err
	}
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.MD5, md5HexDigest(body))
	if err != nil {
		return "", fmt.Errorf("lianlian 签名失败: %w", err)
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

// Verify 校验 Base64(RSA-MD5(MD5(body).hex)) 签名。
func Verify(publicKeyPEM string, body []byte, signature string) (bool, error) {
	key, err := parsePublicKey(publicKeyPEM)
	if err != nil {
		return false, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signature))
	if err != nil || len(raw) == 0 {
		return false, nil
	}
	if err := rsa.VerifyPKCS1v15(key, crypto.MD5, md5HexDigest(body), raw); err != nil {
		return false, nil
	}
	return true, nil
}

func parsePrivateKey(privateKeyPEM string) (*rsa.PrivateKey, error) {
	trimmed := strings.TrimSpace(privateKeyPEM)
	if trimmed == "" {
		return nil, ErrEmptyPrivateKey
	}
	block, _ := pem.Decode([]byte(trimmed))
	if block == nil {
		return nil, errors.New("lianlian 商户私钥不是有效的 PEM 格式")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("lianlian 商户私钥解析失败: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("lianlian 商户私钥不是 RSA 私钥")
	}
	return key, nil
}

func parsePublicKey(publicKeyPEM string) (*rsa.PublicKey, error) {
	trimmed := strings.TrimSpace(publicKeyPEM)
	if trimmed == "" {
		return nil, ErrEmptyPublicKey
	}
	block, _ := pem.Decode([]byte(trimmed))
	if block == nil {
		return nil, errors.New("lianlian 平台公钥不是有效的 PEM 格式")
	}
	if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
		key, ok := cert.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("lianlian 平台证书不是 RSA 公钥")
		}
		return key, nil
	}
	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("lianlian 平台公钥解析失败: %w", err)
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("lianlian 平台公钥不是 RSA 公钥")
	}
	return key, nil
}

// MarshalBody 序列化创单报文。返回的字节必须同时用于签名与实际发送。
func MarshalBody(params map[string]any) ([]byte, error) {
	return common.Marshal(params)
}
