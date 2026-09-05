"""
Tests for Saucepan models.
"""

from unittest.mock import Mock

import pytest

from saucepan import exceptions, models


class TestTaskSpec:
    """Tests for TaskSpec model."""

    def test_valid_task_spec(self):
        """Test creating a valid task specification."""
        spec = models.TaskSpec(
            name="M42",
            integration_time=300,
            min_power=0.7,
        )

        assert spec.name == "M42"
        assert spec.integration_time == 300
        assert spec.min_power == 0.7
        assert spec.filters is None
        assert spec.priority == 10

    def test_task_spec_with_optional_fields(self):
        """Test task spec with optional fields."""
        spec = models.TaskSpec(
            name="M31",
            integration_time=600,
            min_power=0.8,
            filters=["R", "G", "B"],
            max_psf_fwhm=2.5,
            max_plate_scale=0.5,
            min_aperture_mm=200,
            priority=20,
            description="Andromeda Galaxy",
        )

        assert spec.filters == ["R", "G", "B"]
        assert spec.max_psf_fwhm == 2.5
        assert spec.max_plate_scale == 0.5
        assert spec.min_aperture_mm == 200
        assert spec.priority == 20
        assert spec.description == "Andromeda Galaxy"

    def test_validate_missing_name(self):
        """Test validation catches missing name."""
        spec = models.TaskSpec(
            name="",
            integration_time=300,
            min_power=0.7,
        )

        with pytest.raises(exceptions.ValidationError) as exc:
            spec.validate()

        assert "name" in exc.value.fields

    def test_validate_negative_integration_time(self):
        """Test validation catches negative integration time."""
        spec = models.TaskSpec(
            name="M42",
            integration_time=-100,
            min_power=0.7,
        )

        with pytest.raises(exceptions.ValidationError) as exc:
            spec.validate()

        assert "integration_time" in exc.value.fields

    def test_validate_invalid_power_too_high(self):
        """Test validation catches min_power > 1.0."""
        spec = models.TaskSpec(
            name="M42",
            integration_time=300,
            min_power=1.5,
        )

        with pytest.raises(exceptions.ValidationError) as exc:
            spec.validate()

        assert "min_power" in exc.value.fields

    def test_validate_invalid_power_too_low(self):
        """Test validation catches min_power < 0.0."""
        spec = models.TaskSpec(
            name="M42",
            integration_time=300,
            min_power=-0.1,
        )

        with pytest.raises(exceptions.ValidationError) as exc:
            spec.validate()

        assert "min_power" in exc.value.fields

    def test_to_dict(self):
        """Test converting task spec to dictionary."""
        spec = models.TaskSpec(
            name="M42",
            integration_time=300,
            min_power=0.7,
            filters=["R", "G"],
        )

        d = spec.to_dict()

        assert d["name"] == "M42"
        assert d["integration_time"] == 300
        assert d["min_power"] == 0.7
        assert d["required_filters"] == ["R", "G"]
        assert "priority" in d

    def test_to_dict_omits_unset_optional_fields(self):
        """Bare-minimum spec should not emit optional keys at all."""
        spec = models.TaskSpec(name="M42", integration_time=300, min_power=0.7)
        d = spec.to_dict()
        for key in (
            "required_filters",
            "max_psf_fwhm",
            "max_plate_scale",
            "min_aperture_mm",
            "normalized_integration_budget_s",
        ):
            assert key not in d

    def test_to_dict_includes_all_optional_fields_when_set(self):
        spec = models.TaskSpec(
            name="M42",
            integration_time=300,
            min_power=0.7,
            max_psf_fwhm=2.5,
            max_plate_scale=0.8,
            min_aperture_mm=150,
            normalized_integration_budget_s=7200.0,
        )
        d = spec.to_dict()
        assert d["max_psf_fwhm"] == 2.5
        assert d["max_plate_scale"] == 0.8
        assert d["min_aperture_mm"] == 150
        assert d["normalized_integration_budget_s"] == 7200.0

    def test_validate_boundary_min_power_zero_is_valid(self):
        spec = models.TaskSpec(name="M42", integration_time=300, min_power=0.0)
        spec.validate()  # should not raise

    def test_validate_boundary_min_power_one_is_valid(self):
        spec = models.TaskSpec(name="M42", integration_time=300, min_power=1.0)
        spec.validate()  # should not raise

    def test_validate_max_psf_fwhm_zero_invalid(self):
        spec = models.TaskSpec(name="M42", integration_time=300, min_power=0.7, max_psf_fwhm=0)
        with pytest.raises(exceptions.ValidationError) as exc:
            spec.validate()
        assert "max_psf_fwhm" in exc.value.fields

    def test_validate_max_plate_scale_negative_invalid(self):
        spec = models.TaskSpec(
            name="M42", integration_time=300, min_power=0.7, max_plate_scale=-0.1
        )
        with pytest.raises(exceptions.ValidationError) as exc:
            spec.validate()
        assert "max_plate_scale" in exc.value.fields

    def test_validate_min_aperture_mm_zero_invalid(self):
        spec = models.TaskSpec(name="M42", integration_time=300, min_power=0.7, min_aperture_mm=0)
        with pytest.raises(exceptions.ValidationError) as exc:
            spec.validate()
        assert "min_aperture_mm" in exc.value.fields

    def test_validate_normalized_integration_budget_zero_invalid(self):
        spec = models.TaskSpec(
            name="M42",
            integration_time=300,
            min_power=0.7,
            normalized_integration_budget_s=0,
        )
        with pytest.raises(exceptions.ValidationError) as exc:
            spec.validate()
        assert "normalized_integration_budget_s" in exc.value.fields

    def test_validate_multiple_errors_all_reported(self):
        spec = models.TaskSpec(name="", integration_time=-1, min_power=2.0)
        with pytest.raises(exceptions.ValidationError) as exc:
            spec.validate()
        assert set(exc.value.fields) == {"name", "integration_time", "min_power"}


class TestTask:
    """Tests for Task model."""

    def test_task_status_methods(self):
        """Test task status checking methods."""
        task = models.Task(
            id="123",
            name="M42",
            status="pending",
            spec=models.TaskSpec(name="M42", integration_time=300, min_power=0.7),
        )

        assert task.is_pending is True
        assert task.is_complete is False
        assert task.is_failed is False

        # Test completed status
        task = models.Task(
            id="123",
            name="M42",
            status="completed",
            spec=models.TaskSpec(name="M42", integration_time=300, min_power=0.7),
        )

        assert task.is_pending is False
        assert task.is_complete is True
        assert task.is_failed is False

    def test_task_all_pending_states(self):
        """Test all pending states."""
        for status in ["pending", "assigned", "in_progress"]:
            task = models.Task(
                id="123",
                name="M42",
                status=status,
                spec=models.TaskSpec(name="M42", integration_time=300, min_power=0.7),
            )
            assert task.is_pending is True


class TestDelivery:
    """Tests for Delivery model."""

    def test_delivery_completed(self, sample_task_spec):
        """Test completed delivery."""
        mock_http = Mock()
        delivery = models.Delivery(
            notification_id=1,
            task_id="123",
            status="completed",
            failure_reason=None,
            _original_spec=sample_task_spec,
            _fits_url="https://example.com/fits/123.fits",
            _http=mock_http,
        )

        assert delivery.status == "completed"
        assert delivery.failure_reason is None

        # Test fits_url property
        assert delivery.fits_url == "https://example.com/fits/123.fits"

        # Test download
        delivery.download("/tmp")
        mock_http.download_fits.assert_called_once()

    def test_delivery_failed(self, sample_task_spec):
        """Test failed delivery."""
        delivery = models.Delivery(
            notification_id=1,
            task_id="123",
            status="failed",
            failure_reason="Telescope malfunction",
            _original_spec=sample_task_spec,
            _fits_url=None,
            _http=Mock(),
        )

        assert delivery.status == "failed"
        assert delivery.failure_reason == "Telescope malfunction"

        # Test fits_url property raises error
        with pytest.raises(exceptions.SaucepanError):
            _ = delivery.fits_url

    def test_delivery_acknowledge(self, sample_task_spec):
        """Test acknowledging delivery."""
        mock_http = Mock()
        delivery = models.Delivery(
            notification_id=1,
            task_id="123",
            status="completed",
            failure_reason=None,
            _original_spec=sample_task_spec,
            _fits_url="https://example.com/fits/123.fits",
            _http=mock_http,
        )

        delivery.acknowledge()
        mock_http.acknowledge_notification.assert_called_once_with(1)

    def test_delivery_completed_but_fits_url_missing_raises(self, sample_task_spec):
        """Status says completed but the server omitted the FITS URL — must
        fail closed with a clear error rather than returning None silently."""
        delivery = models.Delivery(
            notification_id=1,
            task_id="123",
            status="completed",
            failure_reason=None,
            _original_spec=sample_task_spec,
            _fits_url=None,
            _http=Mock(),
        )
        with pytest.raises(exceptions.SaucepanError, match="FITS URL not available"):
            _ = delivery.fits_url

    def test_delivery_repr_hides_signed_url(self, sample_task_spec):
        delivery = models.Delivery(
            notification_id=1,
            task_id="123",
            status="completed",
            failure_reason=None,
            _original_spec=sample_task_spec,
            _fits_url="https://signed.example/secret?token=hidden",
            _http=Mock(),
        )
        assert "signed.example" not in repr(delivery)

    def test_delivery_download_when_not_completed_raises(self, sample_task_spec):
        delivery = models.Delivery(
            notification_id=1,
            task_id="123",
            status="pending",
            failure_reason=None,
            _original_spec=sample_task_spec,
            _fits_url=None,
            _http=Mock(),
        )
        with pytest.raises(exceptions.SaucepanError, match="nothing to download"):
            delivery.download("/tmp")

    def test_delivery_resubmit(self, sample_task_spec):
        """Test resubmitting delivery."""
        mock_http = Mock()
        delivery = models.Delivery(
            notification_id=1,
            task_id="123",
            status="failed",
            failure_reason="Cloud cover",
            _original_spec=sample_task_spec,
            _fits_url=None,
            _http=mock_http,
        )

        delivery.resubmit()
        mock_http.submit_task.assert_called_once_with(sample_task_spec)


class TestQuotaStatus:
    """Tests for QuotaStatus model."""

    def test_quota_remaining(self):
        """Test quota remaining calculation."""
        quota = models.QuotaStatus(
            total=100,
            used=50,
        )

        assert quota.remaining == 50

    def test_quota_is_exhausted(self):
        """Test quota exhausted check."""
        quota = models.QuotaStatus(
            total=100,
            used=100,
        )

        assert quota.is_exhausted is True

        quota = models.QuotaStatus(
            total=100,
            used=50,
        )

        assert quota.is_exhausted is False

    def test_quota_over_exhausted(self):
        """Test quota when used exceeds total."""
        quota = models.QuotaStatus(
            total=100,
            used=150,
        )

        assert quota.remaining == -50
        assert quota.is_exhausted is True
