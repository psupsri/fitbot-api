-- บทวิเคราะห์จาก AI แบบแยกส่วน (summary, advice, watch_explanation) ให้ dashboard แสดงคู่กับกราฟได้
-- NULL = รายงานแบบ template หรือรายงานที่ส่งก่อนมีคอลัมน์นี้
ALTER TABLE daily_reports ADD COLUMN coaching JSONB;
