import os
import shutil
import hashlib
import uuid
from pathlib import Path
from typing import Optional, Dict, Any

from storage.backend import confined_path, safe_path_component


class LocalStorageClient:
    """Local file system storage client for the Saucepan Data Lake"""

    def __init__(self, storage_root: Optional[str] = None):
        if storage_root is None:
            storage_root = os.environ.get("STORAGE_ROOT", "/data")
        self.storage_root = Path(storage_root)
        self.raw_root = self.storage_root / "raw"
        self.processed_root = self.storage_root / "processed"

        # Ensure directories exist
        self.raw_root.mkdir(parents=True, exist_ok=True)
        self.processed_root.mkdir(parents=True, exist_ok=True)

    def upload_to_staging(self, file_path: str, campaign_id: str) -> Dict[str, Any]:
        """
        Upload a file to the raw staging area

        Args:
            file_path: Path to the source file
            campaign_id: Campaign identifier for organization

        Returns:
            Dict with upload status and metadata
        """
        source = Path(file_path)
        if not source.exists():
            return {"success": False, "error": f"Source file not found: {file_path}"}

        # Create campaign directory if it doesn't exist
        safe_campaign = safe_path_component(campaign_id, "campaign_id")
        campaign_dir = confined_path(self.raw_root / "uploads", safe_campaign)
        campaign_dir.mkdir(parents=True, exist_ok=True)

        # Generate unique filename to avoid conflicts
        dest_filename = f"{source.stem}_{uuid.uuid4().hex}_{source.stat().st_size}{source.suffix}"
        dest_path = campaign_dir / dest_filename

        try:
            # Copy file to staging
            shutil.copy2(source, dest_path)
            dest_path.chmod(0o660)

            # Calculate checksum
            checksum = self._calculate_checksum(dest_path)

            return {
                "success": True,
                "staging_path": str(dest_path),
                "file_size": source.stat().st_size,
                "checksum": checksum,
                "original_filename": source.name,
            }

        except Exception as e:
            return {"success": False, "error": str(e)}

    def move_to_campaign(
        self, staging_path: str, campaign_id: str, dataset_name: str
    ) -> Dict[str, Any]:
        """
        Move a file from staging to the processed campaign directory

        Args:
            staging_path: Path to the staged file
            campaign_id: Campaign identifier
            dataset_name: Name for the dataset file

        Returns:
            Dict with move status and metadata
        """
        source = Path(staging_path)
        if not source.exists():
            return {"success": False, "error": f"Staging file not found: {staging_path}"}

        # Create destination path in processed campaigns
        safe_campaign = safe_path_component(campaign_id, "campaign_id")
        safe_dataset = safe_path_component(dataset_name, "dataset_name")
        campaign_dir = confined_path(self.processed_root / "campaigns", safe_campaign)
        campaign_dir.mkdir(parents=True, exist_ok=True)

        # Create sanitized dataset filename
        dest_filename = f"{safe_dataset}{source.suffix}"
        dest_path = confined_path(campaign_dir, dest_filename)

        try:
            # Move file to final location
            shutil.move(str(source), str(dest_path))

            # Calculate checksum for verification
            checksum = self._calculate_checksum(dest_path)

            return {
                "success": True,
                "final_path": str(dest_path),
                "file_size": dest_path.stat().st_size,
                "checksum": checksum,
                "dataset_name": dataset_name,
            }

        except Exception as e:
            return {"success": False, "error": str(e)}

    def calculate_checksum(self, file_path: str, algorithm: str = "sha256") -> Optional[str]:
        """
        Calculate checksum for a file

        Args:
            file_path: Path to the file
            algorithm: Hash algorithm to use (sha256, md5, etc.)

        Returns:
            Hex digest of the checksum or None if error
        """
        return self._calculate_checksum(Path(file_path), algorithm)

    def _calculate_checksum(self, file_path: Path, algorithm: str = "sha256") -> Optional[str]:
        """Internal method to calculate file checksum"""
        hash_func = hashlib.new(algorithm)

        try:
            with open(file_path, "rb") as f:
                for chunk in iter(lambda: f.read(4096), b""):
                    hash_func.update(chunk)
            return hash_func.hexdigest()

        except Exception:
            return None

    def get_file_info(self, file_path: str) -> Dict[str, Any]:
        """
        Get metadata for a file

        Args:
            file_path: Path to the file

        Returns:
            Dict with file metadata
        """
        path = Path(file_path)
        if not path.exists():
            return {"success": False, "error": f"File not found: {file_path}"}

        stat = path.stat()
        return {
            "success": True,
            "path": str(path),
            "size": stat.st_size,
            "modified": stat.st_mtime,
            "checksum": self._calculate_checksum(path),
        }

    def cleanup_old_files(self, days_old: int = 30) -> Dict[str, int]:
        """
        Clean up old files from cache and temporary directories

        Args:
            days_old: Remove files older than this many days

        Returns:
            Dict with counts of cleaned files
        """
        import time

        cutoff_time = time.time() - (days_old * 24 * 60 * 60)

        cleaned_count = 0
        cache_dirs = [
            self.storage_root / "cache" / "temp",
            self.storage_root / "cache" / "processing",
        ]

        for cache_dir in cache_dirs:
            if cache_dir.exists():
                for file_path in cache_dir.glob("*"):
                    if file_path.is_file() and file_path.stat().st_mtime < cutoff_time:
                        try:
                            file_path.unlink()
                            cleaned_count += 1
                        except OSError:
                            pass

        return {"success": True, "files_cleaned": cleaned_count}
