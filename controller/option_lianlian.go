package controller

import (
	"errors"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/lianlianpay"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
)

type saveLianlianSettingsRequest struct {
	Enabled           bool   `json:"enabled"`
	Env               string `json:"env"`
	Gateway           string `json:"gateway"`
	MerchantID        string `json:"merchant_id"`
	PrivateKey        string `json:"private_key"`
	PlatformPublicKey string `json:"platform_public_key"`
	BusiType          string `json:"busi_type"`
	RiskCategory      string `json:"risk_category"`
	ClearPrivateKey   bool   `json:"clear_private_key"`
}

func SaveLianlianSettings(c *gin.Context) {
	var req saveLianlianSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "参数错误"})
		return
	}

	env := strings.ToLower(strings.TrimSpace(req.Env))
	if env != lianlianpay.EnvTest && env != lianlianpay.EnvProd {
		common.ApiErrorMsg(c, "连连环境必须是 test 或 prod")
		return
	}
	gateway := strings.TrimRight(strings.TrimSpace(req.Gateway), "/")
	merchantID := strings.TrimSpace(req.MerchantID)
	busiType := strings.TrimSpace(req.BusiType)
	if busiType == "" {
		busiType = lianlianpay.DefaultBusiType
	}
	riskCategory := strings.TrimSpace(req.RiskCategory)
	if riskCategory == "" {
		riskCategory = lianlianpay.DefaultRiskCategory
	}
	if len(merchantID) > 64 || len(busiType) > 64 || len(riskCategory) > 64 {
		common.ApiErrorMsg(c, "连连配置字段过长")
		return
	}

	privateKey := strings.TrimSpace(req.PrivateKey)
	if req.ClearPrivateKey && privateKey != "" {
		common.ApiErrorMsg(c, "不能同时填写新私钥和清除私钥")
		return
	}
	if privateKey == "" && !req.ClearPrivateKey {
		privateKey = strings.TrimSpace(setting.LianlianPrivateKey)
	}
	platformPublicKey := strings.TrimSpace(req.PlatformPublicKey)
	effectivePublicKey := lianlianpay.ResolvePlatformKey(platformPublicKey)

	if gateway != "" {
		if _, err := lianlianpay.GatewayURL(env, gateway); err != nil {
			common.ApiErrorMsg(c, err.Error())
			return
		}
		if _, err := lianlianpay.QueryGatewayURL(env, gateway); err != nil {
			common.ApiErrorMsg(c, err.Error())
			return
		}
	}
	if privateKey != "" {
		if err := lianlianpay.ValidatePrivateKeyPEM(privateKey); err != nil {
			common.ApiErrorMsg(c, "连连商户私钥无效："+err.Error())
			return
		}
	}
	if platformPublicKey != "" {
		if err := lianlianpay.ValidatePublicKeyPEM(platformPublicKey); err != nil {
			common.ApiErrorMsg(c, "连连平台公钥无效："+err.Error())
			return
		}
	}
	if req.Enabled {
		if merchantID == "" || privateKey == "" {
			common.ApiErrorMsg(c, "启用连连支付前必须配置商户号和商户私钥")
			return
		}
		if err := lianlianpay.ValidatePublicKeyPEM(effectivePublicKey); err != nil {
			common.ApiErrorMsg(c, "连连平台公钥无效："+err.Error())
			return
		}
	}

	values := map[string]string{
		"LianlianEnabled":            common.Interface2String(req.Enabled),
		"LianlianEnv":                env,
		"LianlianGateway":            gateway,
		"LianlianMchId":              merchantID,
		"LianlianPrivateKey":         privateKey,
		"LianlianPlatformPublicCert": platformPublicKey,
		"LianlianBusiType":           busiType,
		"LianlianRiskCategory":       riskCategory,
	}
	if err := model.UpdateLianlianOptionsAtomic(values); err != nil {
		switch {
		case errors.Is(err, model.ErrLianlianConfigOpenOrders):
			common.ApiErrorMsg(c, "存在创建中、待支付、已取消未关闭或仍处于回调重试窗口的连连订单，暂不能切换环境、商户号、网关、商户私钥或平台公钥")
			return
		default:
			logger.LogError(c.Request.Context(), "保存连连支付配置失败: "+err.Error())
			common.ApiErrorMsg(c, "保存连连支付配置失败")
			return
		}
	}

	common.ApiSuccess(c, gin.H{
		"private_key_configured": privateKey != "",
	})
}
