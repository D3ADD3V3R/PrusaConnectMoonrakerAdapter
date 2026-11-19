"""Offline replay of the shipped templates using Klipper's Jinja delimiters.

Requires Jinja2. This checks template/control behavior, not physical extrusion.
"""

import ast
import configparser
import copy
from pathlib import Path
import shlex
import unittest

import jinja2

ROOT = Path(__file__).resolve().parents[1]


class Refused(Exception):
    pass


class Printer:
    def __init__(self, saved=None):
        parser = configparser.RawConfigParser()
        parser.read(ROOT / 'adapter/macroconfig/pcma-filament.cfg')
        self.macros = {section.split(' ', 1)[1]: parser[section]['gcode']
                       for section in parser.sections() if section.startswith(('gcode_macro ', 'delayed_gcode '))}
        state = {key.removeprefix('variable_'): ast.literal_eval(value)
                 for key, value in parser['gcode_macro PCMA_STATE'].items() if key.startswith('variable_')}
        self.printer = {
            'gcode_macro PCMA_STATE': state,
            'save_variables': {'variables': copy.deepcopy(saved or {})},
            'print_stats': {'state': 'standby'}, 'pause_resume': {'is_paused': False},
            'idle_timeout': {'state': 'Ready'}, 'toolhead': {'estimated_print_time': 42},
            'extruder': {'can_extrude': False},
            'configfile': {'settings': {'extruder': {'max_temp': 250, 'min_extrude_temp': 170,
                                                   'max_extrude_only_distance': 50}}},
        }
        self.events = []
        self.fail_save = False
        self.fail_save_number = None
        self.save_count = 0
        self.env = jinja2.Environment('{%', '%}', '{', '}', undefined=jinja2.StrictUndefined)
        for code in self.macros.values():
            self.env.from_string(code)

    @property
    def state(self):
        return self.printer['gcode_macro PCMA_STATE']

    def run(self, script):
        for line in script.splitlines():
            line = line.strip()
            if not line or line.startswith('#'):
                continue
            words = shlex.split(line)
            cmd = words[0]
            params = dict(word.split('=', 1) for word in words[1:] if '=' in word)
            if cmd in self.macros:
                def refuse(message):
                    raise Refused(message)
                rendered = self.env.from_string(self.macros[cmd]).render(
                    printer=copy.deepcopy(self.printer), params=params,
                    rawparams=line.partition(' ')[2], action_raise_error=refuse,
                    action_respond_info=lambda _message: '',
                )
                self.run(rendered)
            elif cmd == 'SET_GCODE_VARIABLE':
                self.printer['gcode_macro ' + params['MACRO']][params['VARIABLE']] = ast.literal_eval(params['VALUE'])
            elif cmd == 'SAVE_VARIABLE':
                self.save_count += 1
                if self.fail_save or self.save_count == self.fail_save_number:
                    raise Refused('disk full')
                self.printer['save_variables']['variables'][params['VARIABLE']] = ast.literal_eval(params['VALUE'])
                self.events.append(('persist', copy.deepcopy(self.printer['save_variables']['variables'])))
            elif cmd in ('M109', 'M104'):
                target = float(next(word[1:] for word in words[1:] if word.startswith('S')))
                self.events.append(('heat', target))
                self.printer['extruder']['can_extrude'] = target >= 170
            elif cmd == 'G1':
                if any(word.startswith(('X', 'Y', 'Z')) for word in words[1:]):
                    raise AssertionError('Unexpected axis movement')
                self.events.append(('extrude', float(next(word[1:] for word in words[1:] if word.startswith('E')))))
            elif cmd == 'PCMA_RESUME_BASE':
                self.events.append(('resume', True))
            elif cmd not in ('M83', 'M400', 'M117', 'SAVE_GCODE_STATE', 'RESTORE_GCODE_STATE'):
                raise AssertionError('Unexpected command: ' + line)


class Macros(unittest.TestCase):
    def ready(self):
        p = Printer()
        p.run('_PCMA_BOOT')
        p.run('PCMA_RESET_FILAMENT')
        p.events.clear()
        return p

    def change(self, p, request='test-1'):
        p.run(f'PCMA_FILAMENT_CHANGE REQUEST={request} MATERIAL=PLA COLOR=C_FF8800')

    def test_change_requires_local_load_and_confirmation(self):
        p = self.ready()
        self.change(p)
        self.assertEqual(p.state['filament_phase'], 'AWAITING_INSERTION')
        self.assertEqual(p.state['loaded_material'], 'UNKNOWN')
        self.assertEqual([v for kind, v in p.events if kind == 'extrude'], [-40])
        self.assertEqual(p.events[0][0], 'persist')
        p.run('PCMA_LOCAL_LOAD')
        p.run('PCMA_PURGE_FILAMENT')
        self.assertEqual(p.state['filament_phase'], 'AWAITING_CONFIRMATION')
        with self.assertRaises(Refused):
            p.run('RESUME')
        p.run('PCMA_CONFIRM_FILAMENT')
        self.assertEqual(p.state['confirmed_change_id'], 'test-1')
        self.assertEqual(p.state['loaded_color'], 'C_FF8800')
        self.assertFalse(any(kind == 'resume' for kind, _ in p.events))
        before = copy.deepcopy(p.events)
        self.change(p)
        self.assertEqual(p.events, before)

    def test_restart_and_reset_never_replay_the_previous_request(self):
        p = self.ready()
        self.change(p)
        restarted = Printer(p.printer['save_variables']['variables'])
        restarted.run('_PCMA_BOOT')
        self.assertEqual(restarted.events, [])
        self.assertEqual(restarted.state['filament_phase'], 'RECOVERY_REQUIRED')
        with self.assertRaises(Refused):
            restarted.run('PCMA_FILAMENT_CHANGE REQUEST=new MATERIAL=PLA COLOR=C_FF8800')
        restarted.run('PCMA_RESET_FILAMENT')
        before = copy.deepcopy(restarted.events)
        self.change(restarted)
        self.assertEqual(restarted.events, before)

    def test_bad_metadata_and_extruder_limits_refuse(self):
        for args in ('REQUEST=a/b MATERIAL=PLA COLOR=C_FF8800',
                     'REQUEST=test MATERIAL=PLA COLOR=C_ZZ8800',
                     'REQUEST=test MATERIAL=UNKNOWN COLOR=C_FF8800',
                     'REQUEST=test MATERIAL=ASA COLOR=C_FF8800'):
            p = self.ready()
            if 'ASA' in args:
                p.printer['configfile']['settings']['extruder']['max_temp'] = 230
            with self.assertRaises(Refused):
                p.run('PCMA_FILAMENT_CHANGE ' + args)
            self.assertEqual(p.events, [])
        p = self.ready()
        p.state['unload_length'] = 51
        with self.assertRaises(Refused):
            self.change(p)
        self.assertFalse(any(kind in ('heat', 'extrude') for kind, _ in p.events))

    def test_failed_persistence_and_active_print_never_actuate(self):
        p = self.ready()
        p.fail_save = True
        with self.assertRaises(Refused):
            self.change(p)
        self.assertEqual(p.events, [])
        p = self.ready()
        p.printer['print_stats']['state'] = 'printing'
        with self.assertRaises(Refused):
            self.change(p)
        self.assertEqual(p.events, [])

    def test_minimal_printer_without_lcd_or_virtual_sd(self):
        p = self.ready()
        del p.printer['print_stats']
        self.change(p)
        self.assertEqual(p.state['filament_phase'], 'AWAITING_INSERTION')
        p.printer['idle_timeout']['state'] = 'Printing'
        with self.assertRaises(Refused):
            p.run('PCMA_LOCAL_LOAD')

    def test_separate_unload_blocks_resume_even_while_nozzle_is_still_hot(self):
        p = self.ready()
        self.change(p)
        p.run('PCMA_LOCAL_LOAD')
        p.run('PCMA_CONFIRM_FILAMENT')
        p.printer['pause_resume']['is_paused'] = True
        p.printer['print_stats']['state'] = 'paused'
        p.run('PCMA_UNLOAD_FILAMENT')
        p.printer['extruder']['can_extrude'] = True
        self.assertEqual(p.state['filament_phase'], 'UNLOADED')
        with self.assertRaises(Refused):
            p.run('RESUME')
        before = len([kind for kind, _ in p.events if kind == 'extrude'])
        self.change(p, 'test-2')
        self.assertEqual(len([kind for kind, _ in p.events if kind == 'extrude']), before)

    def test_failed_unload_or_load_record_cannot_become_a_second_load(self):
        p = self.ready()
        p.fail_save_number = p.save_count + 2
        with self.assertRaises(Refused):
            self.change(p)
        p.fail_save_number = None
        with self.assertRaises(Refused):
            p.run('PCMA_LOCAL_LOAD')
        self.assertFalse(any(kind == 'extrude' for kind, _ in p.events))
        p = self.ready()
        self.change(p)
        p.fail_save_number = p.save_count + 2
        with self.assertRaises(Refused):
            p.run('PCMA_LOCAL_LOAD')
        p.fail_save_number = None
        before = copy.deepcopy(p.events)
        with self.assertRaises(Refused):
            p.run('PCMA_LOCAL_LOAD')
        self.assertEqual(p.events, before)

    def test_unload_uses_old_material_and_load_uses_requested_material(self):
        p = self.ready()
        p.state['loaded_material'] = 'PLA'
        p.run('PCMA_FILAMENT_CHANGE REQUEST=petg MATERIAL=PETG COLOR=C_FF8800')
        self.assertEqual([value for kind, value in p.events if kind == 'heat' and value > 0], [210])
        p.run('PCMA_LOCAL_LOAD')
        self.assertEqual([value for kind, value in p.events if kind == 'heat' and value > 0], [210,235])


if __name__ == '__main__':
    unittest.main()
