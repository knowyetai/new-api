package setting

import "github.com/QuantumNous/new-api/service/lianlianpay"

// 连连收单配置。默认使用官方测试网关与文档公共测试商户所对应的空凭证：
// 只有管理员填好商户号与私钥后支付入口才会出现。
var (
	// LianlianEnabled 是连连支付的启用开关。
	LianlianEnabled bool
	// LianlianEnv 取值 test/prod，决定默认网关。
	LianlianEnv = lianlianpay.EnvTest
	// LianlianGateway 为空时使用官方网关，可填自建代理或自选网关。
	LianlianGateway string
	// LianlianMchId 是连连商户号。
	LianlianMchId string
	// LianlianPrivateKey 是商户 RSA 私钥（PEM），用于下单签名。
	LianlianPrivateKey string
	// LianlianPlatformPublicCert 是连连平台公钥（PEM），用于回调验签；
	// 留空时使用内置的官方公钥。
	LianlianPlatformPublicCert string
	// LianlianBusiType 是业务类型，默认普通收款 100001。
	LianlianBusiType = lianlianpay.DefaultBusiType
	// LianlianRiskCategory 是连连为商户配置的商品类目编码。
	LianlianRiskCategory = lianlianpay.DefaultRiskCategory
)

// GetLianlianPlatformPublicKey 返回生效的连连平台公钥：管理员配置优先。
func GetLianlianPlatformPublicKey() string {
	return lianlianpay.ResolvePlatformKey(LianlianPlatformPublicCert)
}
