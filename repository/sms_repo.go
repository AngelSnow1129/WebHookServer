package repository

import (
	"smsserver/model"

	"gorm.io/gorm"
)

// SMSRepository 短信数据仓储
type SMSRepository struct {
	db *gorm.DB
}

// NewSMSRepository 创建仓储实例
func NewSMSRepository(db *gorm.DB) *SMSRepository {
	return &SMSRepository{db: db}
}

// Create 插入短信记录
func (r *SMSRepository) Create(record *model.SMSRecord) error {
	return r.db.Create(record).Error
}

// FindByRecipient 按手机号查询短信记录
func (r *SMSRepository) FindByRecipient(recipient string) ([]model.SMSRecord, error) {
	var records []model.SMSRecord
	err := r.db.Where("recipient = ?", recipient).
		Order("created_at DESC").
		Find(&records).Error
	return records, err
}
