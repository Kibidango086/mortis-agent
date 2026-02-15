---
name: hardware
description: I2C and SPI device control
---

# Hardware

Interact with I2C/SPI devices.

## Commands

```bash
# I2C
i2c detect
i2c scan (bus: "1")
i2c read (bus: "1", address: 0x38, register: 0xAC, length: 6)

# SPI
spi list
spi read (device: "2.0", length: 4)
```

## References

- `references/board-pinout.md`
- `references/common-devices.md`
