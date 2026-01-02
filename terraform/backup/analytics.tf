# Storage Class Analysis helps tune lifecycle transitions.
resource "aws_s3_bucket_analytics_configuration" "restic" {
  bucket = aws_s3_bucket.backup.id
  name   = "restic-storage-class-analysis"

  filter {
    prefix = "restic/"
  }

  storage_class_analysis {
    data_export {
      output_schema_version = "V_1"
      destination {
        s3_bucket_destination {
          bucket_arn = aws_s3_bucket.backup.arn
          format     = "CSV"
          prefix     = "analytics/storage-class/restic/"
        }
      }
    }
  }
}
